//go:build integration

package integration

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

func historyChanges(t *testing.T, e *env, taskID uuid.UUID, version int) map[string]history.Change {
	t.Helper()
	var raw []byte
	if err := e.db.QueryRow(`SELECT changes FROM task_history WHERE task_id = ? AND task_version = ?`,
		platform.IDBytes(taskID), version).Scan(&raw); err != nil {
		t.Fatalf("история v%d: %v", version, err)
	}
	var m map[string]history.Change
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateTaskDefaultsAndHistory(t *testing.T) {
	e := newEnv(t)
	owner, member, stranger := e.user("O"), e.user("M"), e.user("S")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{member: team.RoleMember})
	other := e.teamWith(stranger, nil)

	// Минимальное тело: автор — текущий пользователь, значения по умолчанию, история v1, ревизия кеша.
	rev := e.revision(tm)
	tk := e.createTask(member, tm, "Минимальная", nil)
	if tk.CreatedBy != member || tk.Status != task.StatusTodo || tk.Description != "" || tk.AssigneeID != nil ||
		tk.Version != 1 || tk.ClosedAt != nil || !tk.CreatedAt.Equal(tk.UpdatedAt) {
		t.Fatalf("значения по умолчанию: %+v", tk)
	}
	ch := historyChanges(t, e, tk.ID, 1)
	if ch["title"].Old != nil || ch["title"].New != "Минимальная" || ch["created_by"].New != member.String() ||
		ch["assignee_id"].New != nil || ch["status"].New != "todo" {
		t.Fatalf("начальная запись истории: %#v", ch)
	}
	if e.revision(tm) != rev+1 {
		t.Fatal("создание задачи должно увеличить ревизию списков команды")
	}

	// Создание сразу в done ставит closed_at; исполнитель из команды допустим.
	done, err := e.tasks.Create(e.ctx, owner, task.CreateInput{TeamID: tm, Title: "Готово", Status: "done", AssigneeID: &member})
	if err != nil || done.ClosedAt == nil || !done.ClosedAt.Equal(done.CreatedAt) || *done.AssigneeID != member {
		t.Fatalf("создание в done: %+v %v", done, err)
	}
	if historyChanges(t, e, done.ID, 1)["closed_at"].New == nil {
		t.Fatal("журнал создания в done должен содержать closed_at")
	}

	// Исполнитель вне команды и неучастник: ничего не записано.
	tasks, history := e.count(`SELECT COUNT(*) FROM tasks`), e.count(`SELECT COUNT(*) FROM task_history`)
	if _, err := e.tasks.Create(e.ctx, owner, task.CreateInput{TeamID: tm, Title: "X", AssigneeID: &stranger}); !errors.Is(err, task.ErrAssigneeNotInTeam) {
		t.Fatalf("исполнитель вне команды: %v", err)
	}
	if _, err := e.tasks.Create(e.ctx, member, task.CreateInput{TeamID: other, Title: "X"}); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("чужая команда: %v", err)
	}
	if _, err := e.tasks.Create(e.ctx, member, task.CreateInput{TeamID: mustID(t), Title: "X"}); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("несуществующая команда: %v", err)
	}
	if e.count(`SELECT COUNT(*) FROM tasks`) != tasks || e.count(`SELECT COUNT(*) FROM task_history`) != history {
		t.Fatal("отказ создания оставил записи")
	}
}

// Ошибка записи истории (настоящая ошибка MySQL) откатывает уже выполненный UPDATE задачи и
// вставку новой задачи: задача, версия, история и ревизия кеша остаются прежними.
func TestHistoryFailureRollsBackUpdate(t *testing.T) {
	e := newEnv(t)
	owner := e.user("O")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "Исходный", nil)
	rev := e.revision(tm)

	restore := testutil.FailInserts(t, e.cfg, "task_history")
	if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("Новый"), Status: task.Set("done")}); err == nil {
		t.Fatal("ожидалась ошибка записи истории")
	}
	if _, err := e.tasks.Create(e.ctx, owner, task.CreateInput{TeamID: tm, Title: "Не создастся"}); err == nil {
		t.Fatal("ожидалась ошибка записи истории при создании")
	}
	restore()

	got, err := e.tasks.Get(e.ctx, owner, tk.ID)
	if err != nil || got.Title != "Исходный" || got.Version != 1 || got.Status != task.StatusTodo || got.ClosedAt != nil {
		t.Fatalf("задача изменилась несмотря на откат: %+v %v", got, err)
	}
	if e.historyCount(tk.ID) != 1 || e.revision(tm) != rev || e.count(`SELECT COUNT(*) FROM tasks`) != 1 {
		t.Fatal("история, ревизия кеша или задачи изменились после отката")
	}
}

func TestUpdateVersionAndStatusRules(t *testing.T) {
	e := newEnv(t)
	owner, author, assignee, other := e.user("O"), e.user("A"), e.user("E"), e.user("X")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{author: team.RoleMember, assignee: team.RoleMember, other: team.RoleMember})
	tk := e.createTask(author, tm, "T", &assignee)

	// Права: исполнитель меняет только status; запрещённое поле с прежним значением — тоже 403.
	if _, err := e.tasks.Update(e.ctx, assignee, tk.ID, task.UpdateInput{Version: 1, Status: task.Set("in_progress"), Title: task.Set("T")}); !errors.Is(err, task.ErrForbidden) {
		t.Fatalf("исполнитель с title: %v", err)
	}
	if _, err := e.tasks.Update(e.ctx, other, tk.ID, task.UpdateInput{Version: 1, Status: task.Set("done")}); !errors.Is(err, task.ErrForbidden) {
		t.Fatalf("посторонний участник: %v", err)
	}
	// Права проверяются до версии: устаревшая версия постороннему не раскрывается.
	if _, err := e.tasks.Update(e.ctx, other, tk.ID, task.UpdateInput{Version: 99, Status: task.Set("done")}); !errors.Is(err, task.ErrForbidden) {
		t.Fatalf("посторонний со старой версией: %v", err)
	}
	if e.version(tk.ID) != 1 || e.historyCount(tk.ID) != 1 {
		t.Fatal("отказ в правах изменил задачу")
	}

	// Закрытие исполнителем ставит closed_at; история содержит status и closed_at.
	clock := e.clock
	clock.Advance(time.Hour)
	closed, err := e.tasks.Update(e.ctx, assignee, tk.ID, task.UpdateInput{Version: 1, Status: task.Set("done")})
	if err != nil || closed.Version != 2 || closed.ClosedAt == nil || !closed.ClosedAt.Equal(clock.Now()) {
		t.Fatalf("закрытие: %+v %v", closed, err)
	}
	if ch := historyChanges(t, e, tk.ID, 2); ch["status"].New != "done" || ch["closed_at"].New == nil {
		t.Fatalf("журнал закрытия: %#v", ch)
	}
	// Редактирование закрытой задачи автором не сдвигает closed_at.
	clock.Advance(time.Hour)
	edited, err := e.tasks.Update(e.ctx, author, tk.ID, task.UpdateInput{Version: 2, Title: task.Set("T2")})
	if err != nil || !edited.ClosedAt.Equal(*closed.ClosedAt) || edited.Status != task.StatusDone {
		t.Fatalf("редактирование done: %+v %v", edited, err)
	}
	// Открытие очищает closed_at, повторное закрытие ставит новое время.
	clock.Advance(time.Hour)
	reopened, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 3, Status: task.Set("in_progress")})
	if err != nil || reopened.ClosedAt != nil {
		t.Fatalf("открытие: %+v %v", reopened, err)
	}
	clock.Advance(time.Hour)
	again, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 4, Status: task.Set("done")})
	if err != nil || !again.ClosedAt.Equal(clock.Now()) || again.Version != 5 {
		t.Fatalf("повторное закрытие: %+v %v", again, err)
	}

	// Устаревшая версия — 409 с expected/current, даже при совпадающих значениях.
	var vc *task.VersionConflictError
	if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 2, Status: task.Set("done")}); !errors.As(err, &vc) ||
		vc.Expected != 2 || vc.Current != 5 {
		t.Fatalf("устаревшая версия: %v", err)
	}
	if e.version(tk.ID) != 5 || e.historyCount(tk.ID) != 5 {
		t.Fatal("конфликт изменил задачу или историю")
	}

	// Несуществующая задача и неучастник — ErrNotFound.
	if _, err := e.tasks.Update(e.ctx, owner, mustID(t), task.UpdateInput{Version: 1, Title: task.Set("x")}); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("нет задачи: %v", err)
	}
	outsider := e.user("Out")
	if _, err := e.tasks.Update(e.ctx, outsider, tk.ID, task.UpdateInput{Version: 5, Title: task.Set("x")}); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("неучастник: %v", err)
	}
	// Исполнитель вне команды при обновлении.
	if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 5, AssigneeID: task.Set(outsider)}); !errors.Is(err, task.ErrAssigneeNotInTeam) {
		t.Fatalf("назначение неучастника: %v", err)
	}
	// Явный null снимает исполнителя.
	unassigned, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 5, AssigneeID: task.Null[uuid.UUID]()})
	if err != nil || unassigned.AssigneeID != nil {
		t.Fatalf("снятие исполнителя: %+v %v", unassigned, err)
	}
	if ch := historyChanges(t, e, tk.ID, 6); ch["assignee_id"] != (history.Change{Old: assignee.String(), New: nil}) {
		t.Fatalf("журнал снятия: %#v", ch)
	}
}

// Актуальная версия без фактических изменений: 200 без новой версии, updated_at, истории и ревизии.
func TestNoOpUpdateKeepsVersion(t *testing.T) {
	e := newEnv(t)
	owner := e.user("O")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", &owner)
	rev := e.revision(tm)
	e.clock.Advance(time.Hour)

	got, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("T"), Status: task.Set("todo"),
		Description: task.Set(""), AssigneeID: task.Set(owner)})
	if err != nil || got.Version != 1 || !got.UpdatedAt.Equal(tk.UpdatedAt) {
		t.Fatalf("no-op: %+v %v", got, err)
	}
	if e.version(tk.ID) != 1 || e.historyCount(tk.ID) != 1 || e.revision(tm) != rev {
		t.Fatal("no-op изменил версию, историю или ревизию кеша")
	}
	// Совпадающие значения при устаревшей версии — 409, а не no-op.
	if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("T2")}); err != nil {
		t.Fatal(err)
	}
	var vc *task.VersionConflictError
	if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("T2")}); !errors.As(err, &vc) || vc.Current != 2 {
		t.Fatalf("совпадающие значения со старой версией: %v", err)
	}
}

// Две транзакции с одной клиентской версией на отдельных соединениях: обе прочитали задачу
// (барьер перед условным UPDATE), затем одна фиксирует изменение, другая получает 409.
func TestConcurrentUpdateSameVersion(t *testing.T) {
	cases := map[string]struct {
		sameActor bool
		a, b      task.UpdateInput
	}{
		"одно поле":            {a: task.UpdateInput{Version: 1, Title: task.Set("A")}, b: task.UpdateInput{Version: 1, Title: task.Set("B")}},
		"разные поля":          {a: task.UpdateInput{Version: 1, Title: task.Set("A")}, b: task.UpdateInput{Version: 1, Status: task.Set("done")}},
		"один автор, два окна": {sameActor: true, a: task.UpdateInput{Version: 1, Description: task.Set("окно 1")}, b: task.UpdateInput{Version: 1, Status: task.Set("in_progress")}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			owner, admin := e.user("O"), e.user("A")
			tm := e.teamWith(owner, map[uuid.UUID]team.Role{admin: team.RoleAdmin})
			tk := e.createTask(owner, tm, "Исходный", nil)
			rev := e.revision(tm)

			// Барьер: обе транзакции прочитали задачу и проверили версию, прежде чем любая выполнит UPDATE.
			var arrived sync.WaitGroup
			arrived.Add(2)
			release := make(chan struct{})
			svc := e.taskService(hookStore{Store: mysqlstore.TaskStore{DB: e.db}, beforeUpdate: func() {
				arrived.Done()
				<-release
			}})
			second := admin
			if c.sameActor {
				second = owner
			}

			type result struct {
				task task.Task
				err  error
			}
			results := make(chan result, 2)
			for _, run := range []struct {
				actor uuid.UUID
				in    task.UpdateInput
			}{{owner, c.a}, {second, c.b}} {
				go func() {
					got, err := svc.Update(e.ctx, run.actor, tk.ID, run.in)
					results <- result{got, err}
				}()
			}
			arrived.Wait()
			close(release)

			var ok, conflicts int
			for range 2 {
				r := <-results
				var vc *task.VersionConflictError
				switch {
				case r.err == nil:
					ok++
					if r.task.Version != 2 {
						t.Errorf("победитель: версия %d", r.task.Version)
					}
				case errors.As(r.err, &vc):
					conflicts++
					if vc.Expected != 1 || vc.Current != 2 {
						t.Errorf("конфликт: %+v", vc)
					}
				default:
					t.Errorf("неожиданная ошибка: %v", r.err)
				}
			}
			if ok != 1 || conflicts != 1 {
				t.Fatalf("успехов %d, конфликтов %d; ожидалось 1 и 1", ok, conflicts)
			}
			if e.version(tk.ID) != 2 || e.historyCount(tk.ID) != 2 {
				t.Fatalf("версия %d, записей истории %d", e.version(tk.ID), e.historyCount(tk.ID))
			}
			if e.revision(tm) != rev+1 {
				t.Fatalf("ревизия кеша %d, ожидалась %d: проигравший не должен её менять", e.revision(tm), rev+1)
			}
		})
	}
}

// Запись между SELECT и UPDATE: после того как запрос прочитал задачу с совпавшей версией, другая
// транзакция фиксирует новую версию. Условный UPDATE не находит строку — 409 без истории; сервер
// не повторяет запрос сам. Клиент заново читает задачу и осознанно повторяет правку.
func TestWriteBetweenSelectAndUpdateIs409(t *testing.T) {
	e := newEnv(t)
	owner := e.user("O")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "Исходный", nil)

	var once sync.Once
	var interleaveErr error
	var svc *task.Service
	svc = e.taskService(hookStore{Store: mysqlstore.TaskStore{DB: e.db}, beforeUpdate: func() {
		once.Do(func() {
			// Отдельная транзакция на другом соединении фиксирует изменение той же версии.
			_, interleaveErr = e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Status: task.Set("in_progress")})
		})
	}})

	var vc *task.VersionConflictError
	_, err := svc.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("Позднее")})
	if interleaveErr != nil {
		t.Fatalf("промежуточная запись: %v", interleaveErr)
	}
	if !errors.As(err, &vc) || vc.Expected != 1 || vc.Current != 2 {
		t.Fatalf("ожидался 409 expected=1 current=2, получено %v", err)
	}
	got, _ := e.tasks.Get(e.ctx, owner, tk.ID)
	if got.Title != "Исходный" || got.Status != task.StatusInProgress || got.Version != 2 || e.historyCount(tk.ID) != 2 {
		t.Fatalf("после конфликта: %+v, история %d", got, e.historyCount(tk.ID))
	}

	// Осознанный повтор клиента после нового чтения с актуальной версией.
	retried, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: got.Version, Title: task.Set("Позднее")})
	if err != nil || retried.Version != 3 || retried.Status != task.StatusInProgress || retried.Title != "Позднее" {
		t.Fatalf("повтор: %+v %v", retried, err)
	}
}

// Каждая успешная ревизия имеет ровно одну запись истории, old совпадает с состоянием до ревизии;
// конфликты и no-op записей не создают.
func TestHistoryOnlyForSuccessfulRevisions(t *testing.T) {
	e := newEnv(t)
	owner := e.user("O")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "v1", nil)

	steps := []struct {
		in      task.UpdateInput
		success bool
	}{
		{task.UpdateInput{Version: 1, Title: task.Set("v2")}, true},
		{task.UpdateInput{Version: 1, Title: task.Set("stale")}, false},
		{task.UpdateInput{Version: 2, Title: task.Set("v2")}, true}, // no-op
		{task.UpdateInput{Version: 2, Title: task.Set("v3"), AssigneeID: task.Set(owner)}, true},
		{task.UpdateInput{Version: 3, Status: task.Set("done")}, true},
	}
	for i, s := range steps {
		_, err := e.tasks.Update(e.ctx, owner, tk.ID, s.in)
		if (err == nil) != s.success {
			t.Fatalf("шаг %d: %v", i, err)
		}
	}
	if n := e.historyCount(tk.ID); n != 4 {
		t.Fatalf("записей истории %d, ожидалось 4 (создание и три фактических изменения)", n)
	}
	page, err := e.tasks.History(e.ctx, owner, tk.ID, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, h := range page.Items {
		if h.TaskVersion != int64(i+1) {
			t.Fatalf("порядок ревизий: %d на позиции %d", h.TaskVersion, i)
		}
	}
	if ch := historyChanges(t, e, tk.ID, 3); ch["title"] != (history.Change{Old: "v2", New: "v3"}) ||
		ch["assignee_id"] != (history.Change{Old: nil, New: owner.String()}) || len(ch) != 2 {
		t.Fatalf("журнал с несколькими полями: %#v", ch)
	}
	if ch := historyChanges(t, e, tk.ID, 4); ch["status"] != (history.Change{Old: "todo", New: "done"}) || ch["closed_at"].Old != nil {
		t.Fatalf("журнал закрытия: %#v", ch)
	}
}

func TestOffsetListFiltersAndTotal(t *testing.T) {
	e := newEnv(t)
	owner, m := e.user("O"), e.user("M")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{m: team.RoleMember})
	other := e.teamWith(e.user("Other"), nil)

	var all []task.Task
	for i := range 7 {
		var a *uuid.UUID
		if i%2 == 0 {
			a = &m
		}
		all = append(all, e.createTask(owner, tm, "T", a))
	}
	if _, err := e.tasks.Update(e.ctx, owner, all[0].ID, task.UpdateInput{Version: 1, Status: task.Set("done")}); err != nil {
		t.Fatal(err)
	}

	page, err := e.tasks.List(e.ctx, m, task.ListQuery{TeamID: tm, Limit: 3})
	if err != nil || page.Total != 7 || len(page.Items) != 3 || page.Mode != task.PageOffset {
		t.Fatalf("первая страница: %+v %v", page, err)
	}
	for i := 1; i < len(page.Items); i++ {
		if platform.IDString(page.Items[i-1].ID) <= platform.IDString(page.Items[i].ID) {
			t.Fatal("порядок id DESC нарушен")
		}
	}
	off := 6
	if page, _ := e.tasks.List(e.ctx, m, task.ListQuery{TeamID: tm, Limit: 3, Offset: &off}); len(page.Items) != 1 || page.Items[0].ID != all[0].ID {
		t.Fatalf("последняя страница: %+v", page.Items)
	}
	// Фильтры объединяются по И.
	page, _ = e.tasks.List(e.ctx, m, task.ListQuery{TeamID: tm, Limit: 20, Status: "todo", AssigneeID: &m})
	if page.Total != 3 || len(page.Items) != 3 {
		t.Fatalf("todo И assignee: total=%d", page.Total)
	}
	if page, _ := e.tasks.List(e.ctx, m, task.ListQuery{TeamID: tm, Limit: 20, Status: "done"}); page.Total != 1 {
		t.Fatalf("done: total=%d", page.Total)
	}
	// Чужая команда — ErrNotFound; некорректные параметры — ValidationError.
	if _, err := e.tasks.List(e.ctx, m, task.ListQuery{TeamID: other, Limit: 20}); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("чужая команда: %v", err)
	}
	neg := -1
	for name, q := range map[string]task.ListQuery{
		"limit 0":         {TeamID: tm, Limit: 0},
		"limit 101":       {TeamID: tm, Limit: 101},
		"offset < 0":      {TeamID: tm, Limit: 1, Offset: &neg},
		"статус":          {TeamID: tm, Limit: 1, Status: "closed"},
		"режим":           {TeamID: tm, Limit: 1, Mode: "page"},
		"cursor в offset": {TeamID: tm, Limit: 1, Cursor: "x"},
		"offset в cursor": {TeamID: tm, Limit: 1, Mode: task.PageCursor, Offset: &off},
	} {
		var ve *platform.ValidationError
		if _, err := e.tasks.List(e.ctx, m, q); !errors.As(err, &ve) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Курсор задач: UUIDv7 одной временной части, created_at в обратном порядке и перемешанная вставка;
// все задачи читаются маленькими страницами ровно один раз в порядке id DESC. Вставка задачи с
// бо́льшим ID после первой страницы не вызывает повторов.
func TestCursorPaginationStableOrder(t *testing.T) {
	e := newEnv(t)
	owner := e.user("O")
	tm := e.teamWith(owner, nil)

	ids := testutil.SameMillisV7(t, e.clock.Now().UnixMilli(), 9)
	for i, id := range ids { // порядок вставки случаен относительно порядка ID
		e.ids.Push(id, mustID(t))
		e.clock.Set(time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Minute))
		e.createTask(owner, tm, "T", nil)
	}
	want := sortedDesc(ids)

	var seen []uuid.UUID
	page, err := e.tasks.List(e.ctx, owner, task.ListQuery{TeamID: tm, Mode: task.PageCursor, Limit: 2})
	if err != nil || page.Total != 0 || page.NextCursor == nil {
		t.Fatalf("первая страница: %+v %v", page, err)
	}
	for _, it := range page.Items {
		seen = append(seen, it.ID)
	}
	// Задача с бо́льшим ID появляется перед прочитанной страницей.
	newer := e.createTask(owner, tm, "Новая", nil)

	for page.NextCursor != nil {
		cur := *page.NextCursor
		page, err = e.tasks.List(e.ctx, owner, task.ListQuery{TeamID: tm, Mode: task.PageCursor, Limit: 2, Cursor: cur})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range page.Items {
			if it.ID == newer.ID {
				t.Fatal("задача с бо́льшим ID не должна попасть на следующие страницы")
			}
			seen = append(seen, it.ID)
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("прочитано %d задач, ожидалось %d", len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("позиция %d: %s, ожидался %s", i, seen[i], want[i])
		}
	}

	// Курсор привязан к выборке: другая команда/фильтр и повреждение — ErrInvalidCursor.
	first, _ := e.tasks.List(e.ctx, owner, task.ListQuery{TeamID: tm, Mode: task.PageCursor, Limit: 2})
	other := e.teamWith(owner, nil)
	for name, q := range map[string]task.ListQuery{
		"другая команда": {TeamID: other, Mode: task.PageCursor, Limit: 2, Cursor: *first.NextCursor},
		"другой фильтр":  {TeamID: tm, Mode: task.PageCursor, Limit: 2, Status: "todo", Cursor: *first.NextCursor},
		"повреждение":    {TeamID: tm, Mode: task.PageCursor, Limit: 2, Cursor: *first.NextCursor + "x"},
	} {
		if _, err := e.tasks.List(e.ctx, owner, q); !errors.Is(err, task.ErrInvalidCursor) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Смена limit допускается.
	if _, err := e.tasks.List(e.ctx, owner, task.ListQuery{TeamID: tm, Mode: task.PageCursor, Limit: 5, Cursor: *first.NextCursor}); err != nil {
		t.Errorf("смена limit: %v", err)
	}
}

func sortedDesc(ids []uuid.UUID) []uuid.UUID {
	out := append([]uuid.UUID(nil), ids...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if platform.IDString(out[j]) > platform.IDString(out[i]) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
