//go:build integration

package integration

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/secunda/team-tasks-service/internal/cache"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// cached — сервис задач с кешем списков поверх store и клиента rc.
func (e *env) cached(store task.Store, rc *redis.Client, logger *slog.Logger) (*task.Service, *cache.ListCache) {
	lc := cache.NewListCache(rc, e.cfg, e.clock, logger)
	return e.taskService(store).WithListCache(lc), lc
}

// sharedRedis — общий тестовый Redis; ключи команд теста удаляются после теста.
func (e *env) sharedRedis() *redis.Client {
	rc := testutil.Redis(e.t)
	e.t.Cleanup(func() { testutil.DeleteTeamCacheKeys(e.t, e.db, rc) })
	return rc
}

func (e *env) list(svc *task.Service, actor, teamID uuid.UUID, status string) task.Page {
	e.t.Helper()
	p, err := svc.List(e.ctx, actor, task.ListQuery{TeamID: teamID, Status: status, Limit: 20})
	if err != nil {
		e.t.Fatal(err)
	}
	return p
}

func titles(p task.Page) string {
	var out []string
	for _, t := range p.Items {
		out = append(out, t.Title)
	}
	return strings.Join(out, ",")
}

// retitle меняет задачу в обход сервиса и ревизии: если список после этого прежний, он взят из кеша.
func (e *env) retitle(id uuid.UUID, title string) {
	mustExecDB(e.t, e.db, `UPDATE tasks SET title = ? WHERE id = ?`, title, platform.IDBytes(id))
}

// failingHook заставляет выбранные команды Redis завершаться ошибкой, имитируя отказ сервера.
type failingHook struct {
	fail  func(cmd string) bool
	calls atomic.Int64
}

var errRedisDown = errors.New("redis down (test)")

func (h *failingHook) DialHook(next redis.DialHook) redis.DialHook { return next }
func (h *failingHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (h *failingHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		h.calls.Add(1)
		if h.fail != nil && h.fail(cmd.Name()) {
			cmd.SetErr(errRedisDown)
			return errRedisDown
		}
		return next(ctx, cmd)
	}
}

func (e *env) hookedRedis(h *failingHook) *redis.Client {
	rc := redis.NewClient(&redis.Options{Addr: e.cfg.RedisAddr})
	rc.AddHook(h)
	e.t.Cleanup(func() { _ = rc.Close() })
	return rc
}

// После создания или изменения задачи все списки команды перестают использоваться, списки другой
// команды сохраняются; 409 и откат из-за ошибки истории ревизию не меняют.
func TestCacheRevisionInvalidation(t *testing.T) {
	e := newEnv(t)
	rc := e.sharedRedis()
	svc, _ := e.cached(mysqlstore.TaskStore{DB: e.db}, rc, platform.DiscardLogger())
	owner, other := e.user("Owner"), e.user("Other")
	ta, tb := e.teamWith(owner, nil), e.teamWith(other, nil)
	a1 := e.createTask(owner, ta, "A1", nil)
	a2 := e.createTask(owner, ta, "A2", nil)
	b1 := e.createTask(other, tb, "B1", nil)

	// Заполняем кеш: три выборки команды A и одна команды B.
	if got := titles(e.list(svc, owner, ta, "todo")); got != "A2,A1" {
		t.Fatalf("todo: %s", got)
	}
	e.list(svc, owner, ta, "done")
	e.list(svc, owner, ta, "")
	e.list(svc, other, tb, "")
	if n := len(testutil.CacheKeys(t, rc, platform.IDString(ta))); n != 3 {
		t.Fatalf("ключей команды A: %d", n)
	}

	// Повтор выборки до изменений берётся из кеша: правка в обход сервиса не видна.
	e.retitle(a1.ID, "A1-direct")
	e.retitle(b1.ID, "B1-direct")
	if got := titles(e.list(svc, owner, ta, "todo")); got != "A2,A1" {
		t.Fatalf("кеш не использован: %s", got)
	}

	// Изменение задачи сбрасывает все списки команды: и фильтр todo, и done, и полный список.
	revBefore := e.revision(ta)
	done := "done"
	if _, err := svc.Update(e.ctx, owner, a1.ID, task.UpdateInput{Status: task.Set(done), Version: 1}); err != nil {
		t.Fatal(err)
	}
	if e.revision(ta) != revBefore+1 {
		t.Fatal("ревизия не выросла")
	}
	if p := e.list(svc, owner, ta, "todo"); titles(p) != "A2" || p.Total != 1 {
		t.Fatalf("todo после изменения: %s total=%d", titles(p), p.Total)
	}
	if p := e.list(svc, owner, ta, "done"); titles(p) != "A1-direct" || p.Total != 1 {
		t.Fatalf("done после изменения: %s total=%d", titles(p), p.Total)
	}
	// Список другой команды сохранился и по-прежнему берётся из кеша.
	if got := titles(e.list(svc, other, tb, "")); got != "B1" {
		t.Fatalf("кеш команды B сброшен: %s", got)
	}

	// 409 не меняет ни задачу, ни ревизию: прежний кеш продолжает использоваться.
	e.retitle(a2.ID, "A2-direct")
	rev := e.revision(ta)
	if _, err := svc.Update(e.ctx, owner, a2.ID, task.UpdateInput{Status: task.Set(done), Version: 7}); !errors.As(err, new(*task.VersionConflictError)) {
		t.Fatalf("ожидался конфликт: %v", err)
	}
	// Откат из-за настоящей ошибки MySQL при записи истории.
	restore := testutil.FailInserts(t, e.cfg, "task_history")
	if _, err := svc.Update(e.ctx, owner, a2.ID, task.UpdateInput{Status: task.Set(done), Version: 1}); err == nil {
		t.Fatal("ожидалась ошибка записи истории")
	}
	restore()
	if e.revision(ta) != rev {
		t.Fatal("409 или откат изменили ревизию")
	}
	if got := titles(e.list(svc, owner, ta, "todo")); got != "A2" {
		t.Fatalf("после 409 и отката ожидался прежний кеш: %s", got)
	}
	if e.count(`SELECT COUNT(*) FROM tasks WHERE id = ? AND status = 'todo'`, platform.IDBytes(a2.ID)) != 1 {
		t.Fatal("откат не восстановил статус")
	}

	// Создание задачи тоже сбрасывает списки.
	e.createTask(owner, ta, "A3", nil)
	if got := titles(e.list(svc, owner, ta, "todo")); got != "A3,A2-direct" {
		t.Fatalf("после создания: %s", got)
	}
}

// pausingStore останавливает чтение списка после SQL-снимка, но до записи в кеш.
type pausingStore struct {
	task.Store
	read, resume chan struct{}
}

func (s pausingStore) ListOffset(ctx context.Context, userID uuid.UUID, f task.Filter, limit, offset int) ([]task.Task, int, int64, error) {
	items, total, rev, err := s.Store.ListOffset(ctx, userID, f, limit, offset)
	s.read <- struct{}{}
	<-s.resume
	return items, total, rev, err
}

// Чтение, начатое до изменения, записывает старый снимок под старой ревизией, и запрос, начатый
// после изменения, эту запись не использует.
func TestCacheLateFillCannotServeStale(t *testing.T) {
	e := newEnv(t)
	rc := e.sharedRedis()
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", nil)

	store := pausingStore{Store: mysqlstore.TaskStore{DB: e.db}, read: make(chan struct{}), resume: make(chan struct{})}
	slow, _ := e.cached(store, rc, platform.DiscardLogger())
	fresh, _ := e.cached(mysqlstore.TaskStore{DB: e.db}, rc, platform.DiscardLogger())

	var (
		wg      sync.WaitGroup
		oldPage task.Page
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		oldPage, _ = slow.List(e.ctx, owner, task.ListQuery{TeamID: tm, Status: "todo", Limit: 20})
	}()
	<-store.read // старое чтение получило SQL-данные
	done := "done"
	if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Status: task.Set(done), Version: 1}); err != nil {
		t.Fatal(err)
	}
	close(store.resume) // старое чтение записывает кеш уже после изменения
	wg.Wait()

	if titles(oldPage) != "T" {
		t.Fatalf("старое чтение: %s", titles(oldPage))
	}
	if p := e.list(fresh, owner, tm, "todo"); len(p.Items) != 0 || p.Total != 0 {
		t.Fatalf("новый запрос получил устаревший список: %s", titles(p))
	}
	stale := "tasks:v1:" + platform.IDString(tm) + ":" + strconv.Itoa(e.revision(tm)-1) + ":"
	found := false
	for _, k := range testutil.CacheKeys(t, rc, platform.IDString(tm)) {
		found = found || strings.HasPrefix(k, stale)
	}
	if !found {
		t.Fatal("запоздалая запись старого снимка не попала в Redis — сценарий гонки не воспроизведён")
	}
}

// Отказ Redis: списки читаются из MySQL, запись задачи не превращается в ошибку, breaker открывается
// на пятом отказе и обходит Redis; после восстановления старые ключи не выдаются.
func TestCacheOutageAndRecovery(t *testing.T) {
	e := newEnv(t)
	shared := e.sharedRedis()
	var down atomic.Bool
	hook := &failingHook{fail: func(string) bool { return down.Load() }}
	var logs bytes.Buffer
	svc, lc := e.cached(mysqlstore.TaskStore{DB: e.db}, e.hookedRedis(hook), platform.NewLogger(&logs, slog.LevelInfo))
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", nil)

	if got := titles(e.list(svc, owner, tm, "todo")); got != "T" {
		t.Fatal(got)
	}
	staleKeys := testutil.CacheKeys(t, shared, platform.IDString(tm))
	if len(staleKeys) != 1 {
		t.Fatalf("ключи до отказа: %v", staleKeys)
	}

	down.Store(true)
	// Каждый список — неудачные GET и SET: третий запрос открывает breaker на пятом отказе.
	for i := 0; i < 3; i++ {
		if got := titles(e.list(svc, owner, tm, "todo")); got != "T" {
			t.Fatalf("список при отказе Redis: %s", got)
		}
	}
	if lc.Breaker().State() != cache.StateOpen {
		t.Fatalf("breaker: %s", lc.Breaker().State())
	}
	// Изменение во время отказа успешно и видно в списке, хотя Redis не вызывается.
	done := "done"
	if _, err := svc.Update(e.ctx, owner, tk.ID, task.UpdateInput{Status: task.Set(done), Version: 1}); err != nil {
		t.Fatalf("сбой кеша превратился в ошибку записи: %v", err)
	}
	calls := hook.calls.Load()
	if p := e.list(svc, owner, tm, "todo"); len(p.Items) != 0 {
		t.Fatalf("список во время open: %s", titles(p))
	}
	if hook.calls.Load() != calls {
		t.Fatal("в open Redis не обходится")
	}
	if !strings.Contains(logs.String(), `"event":"cache_error"`) || !strings.Contains(logs.String(), `"event":"breaker_open"`) {
		t.Fatalf("сбой и открытие breaker не залогированы:\n%s", logs.String())
	}

	// Redis восстановился со старым ключом; после open-интервала проба закрывает breaker,
	// а старый ключ не выдаётся: изменение произошло под новой ревизией.
	down.Store(false)
	if n, _ := shared.Exists(e.ctx, staleKeys[0]).Result(); n != 1 {
		t.Fatal("старый ключ должен остаться в Redis")
	}
	e.clock.Advance(e.cfg.BreakerOpen)
	if p := e.list(svc, owner, tm, "todo"); len(p.Items) != 0 {
		t.Fatalf("после восстановления выдан старый кеш: %s", titles(p))
	}
	if lc.Breaker().State() != cache.StateClosed {
		t.Fatalf("breaker после успешной пробы: %s", lc.Breaker().State())
	}
	if p := e.list(svc, owner, tm, "done"); titles(p) != "T" {
		t.Fatalf("done после восстановления: %s", titles(p))
	}

	// Недоступный адрес Redis: тот же корректный ответ из MySQL.
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = dead.Close() })
	offline, _ := e.cached(mysqlstore.TaskStore{DB: e.db}, dead, platform.DiscardLogger())
	if p := e.list(offline, owner, tm, "done"); titles(p) != "T" {
		t.Fatalf("без Redis: %s", titles(p))
	}
}

// Сбой только записи в кеш: клиент получает корректный SQL-результат, сбой логируется.
func TestCacheWriteFailureReturnsSQLResult(t *testing.T) {
	e := newEnv(t)
	e.sharedRedis()
	hook := &failingHook{fail: func(cmd string) bool { return cmd == "set" }}
	var logs bytes.Buffer
	svc, _ := e.cached(mysqlstore.TaskStore{DB: e.db}, e.hookedRedis(hook), platform.NewLogger(&logs, slog.LevelInfo))
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	e.createTask(owner, tm, "T", nil)
	if got := titles(e.list(svc, owner, tm, "")); got != "T" {
		t.Fatalf("список: %s", got)
	}
	if !strings.Contains(logs.String(), `"operation":"set"`) || !strings.Contains(logs.String(), `"level":"error"`) {
		t.Fatalf("сбой записи не залогирован:\n%s", logs.String())
	}
}

// TTL 300 секунд задаётся при записи и не продлевается чтением.
func TestCacheTTL(t *testing.T) {
	e := newEnv(t)
	rc := e.sharedRedis()
	svc, _ := e.cached(mysqlstore.TaskStore{DB: e.db}, rc, platform.DiscardLogger())
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	e.createTask(owner, tm, "T", nil)
	e.list(svc, owner, tm, "")
	keys := testutil.CacheKeys(t, rc, platform.IDString(tm))
	if len(keys) != 1 {
		t.Fatalf("ключи: %v", keys)
	}
	first, err := rc.PTTL(e.ctx, keys[0]).Result()
	if err != nil || first > cache.ListTTL || first < cache.ListTTL-5*time.Second {
		t.Fatalf("TTL после записи: %v %v", first, err)
	}
	e.list(svc, owner, tm, "") // попадание
	if again, _ := rc.PTTL(e.ctx, keys[0]).Result(); again > first {
		t.Fatalf("чтение продлило TTL: %v > %v", again, first)
	}
	if n := len(testutil.CacheKeys(t, rc, platform.IDString(tm))); n != 1 {
		t.Fatalf("попадание создало новый ключ: %d", n)
	}
}

// Вытеснение на отдельном Redis с маленьким maxmemory и allkeys-lfu: промах обрабатывается как обычный,
// ответ берётся из MySQL, breaker остаётся закрытым.
func TestEvictionIsCacheMiss(t *testing.T) {
	e := newEnv(t)
	rc := testutil.RedisEvict(t)
	if p := rc.ConfigGet(e.ctx, "maxmemory-policy").Val()["maxmemory-policy"]; p != "allkeys-lfu" {
		t.Fatalf("тестовый Redis для вытеснения должен использовать allkeys-lfu, а не %q", p)
	}
	svc, lc := e.cached(mysqlstore.TaskStore{DB: e.db}, rc, platform.DiscardLogger())
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	for i := 0; i < 3; i++ {
		e.createTask(owner, tm, "T"+strconv.Itoa(i), nil)
	}
	want := titles(e.list(svc, owner, tm, ""))

	// Заполняем бюджет памяти списками фиктивной команды через тот же адаптер.
	filler := platform.IDString(mustID(t))
	t.Cleanup(func() {
		for _, team := range []string{filler, platform.IDString(tm)} {
			if keys := testutil.CacheKeys(t, rc, team); len(keys) > 0 {
				rc.Del(context.Background(), keys...)
			}
		}
	})
	evicted := func() int {
		info, err := rc.Info(e.ctx, "stats").Result()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(info, "\r\n") {
			if v, ok := strings.CutPrefix(line, "evicted_keys:"); ok {
				n, _ := strconv.Atoi(v)
				return n
			}
		}
		t.Fatal("нет evicted_keys")
		return 0
	}
	before := evicted()
	value := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < 400; i++ { // ~25 MiB при maxmemory 8 MiB
		lc.Set(e.ctx, "tasks:v1:"+filler+":0:"+strconv.Itoa(i), value)
	}
	if after := evicted(); after <= before {
		t.Fatalf("вытеснения не было: evicted_keys %d -> %d", before, after)
	}

	// Список корректен независимо от того, вытеснен ли его ключ; breaker закрыт.
	if got := titles(e.list(svc, owner, tm, "")); got != want {
		t.Fatalf("список после вытеснения: %s, ожидалось %s", got, want)
	}
	if lc.Breaker().State() != cache.StateClosed {
		t.Fatalf("вытеснение открыло breaker: %s", lc.Breaker().State())
	}
}
