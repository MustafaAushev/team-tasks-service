//go:build integration

package integration

import (
	"bytes"
	"errors"
	"math"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/statistics"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
)

func day(d, h, m int) time.Time { return time.Date(2026, 3, d, h, m, 0, 0, time.UTC) }

// statsTask — задача сценария с управляемыми моментами создания и изменений.
type statsTask struct {
	e  *env
	by uuid.UUID
	t  task.Task
}

func (e *env) taskAt(at time.Time, actor, teamID uuid.UUID, assignee *uuid.UUID) *statsTask {
	e.t.Helper()
	e.clock.Set(at)
	return &statsTask{e: e, by: actor, t: e.createTask(actor, teamID, "T", assignee)}
}

func (s *statsTask) update(at time.Time, in task.UpdateInput) *statsTask {
	s.e.t.Helper()
	s.e.clock.Set(at)
	in.Version = s.t.Version
	t, err := s.e.tasks.Update(s.e.ctx, s.by, s.t.ID, in)
	if err != nil {
		s.e.t.Fatal(err)
	}
	s.t = t
	return s
}

func (s *statsTask) status(at time.Time, st string) *statsTask {
	return s.update(at, task.UpdateInput{Status: task.Set(st)})
}

func (s *statsTask) assign(at time.Time, to *uuid.UUID) *statsTask {
	return s.update(at, task.UpdateInput{AssigneeID: assigneeField(to)})
}

// assigneeField: nil — снять назначение (null), иначе назначить.
func assigneeField(to *uuid.UUID) task.Field[uuid.UUID] {
	if to == nil {
		return task.Null[uuid.UUID]()
	}
	return task.Set(*to)
}

func (s *statsTask) comments(n int) *statsTask {
	for i := 0; i < n; i++ {
		if _, err := s.e.tasks.AddComment(s.e.ctx, s.by, s.t.ID, "c"); err != nil {
			s.e.t.Fatal(err)
		}
	}
	return s
}

// Отчёт на настоящем MySQL с фиксированным временем: границы окна, ничьи, null-исполнитель,
// снятые назначения бывшего участника, переназначение, повторное открытие и закрытие, комментарии
// без умножения агрегатов и изоляция от второй команды.
func TestTeamReport(t *testing.T) {
	e := newEnv(t)
	owner, admin := e.user("Owner"), e.user("Admin")
	b, c, d, gone := e.user("B"), e.user("C"), e.user("D"), e.user("Gone")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{admin: team.RoleAdmin, b: team.RoleMember, c: team.RoleMember,
		d: team.RoleMember, gone: team.RoleMember})
	stats := statistics.NewService(mysqlstore.StatsStore{DB: e.db}, e.clock)

	asOf := day(31, 12, 0)
	from := asOf.Add(-statistics.TopWindow) // 1 марта 12:00 — нижняя граница включена
	done, todo := "done", "todo"

	// B: закрытие ровно на нижней границе и внутри окна; закрытие на микросекунду раньше границы — нет.
	t1 := e.taskAt(day(20, 12, 0).AddDate(0, -1, 0), owner, tm, &b).status(from, done)
	t2 := e.taskAt(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), owner, tm, &b).status(from.Add(-time.Microsecond), done)
	t5 := e.taskAt(day(24, 0, 0), owner, tm, &b).status(day(25, 6, 0), done).comments(2)
	// Повторно открытая задача B не считается закрытой.
	e.taskAt(day(2, 0, 0), owner, tm, &b).status(day(3, 0, 0), done).status(day(4, 0, 0), todo)
	// C: два закрытия; у одного несколько комментариев — они не умножают агрегаты.
	t3 := e.taskAt(day(9, 0, 0), owner, tm, &c).status(day(10, 0, 0), done).comments(3)
	t4 := e.taskAt(day(19, 0, 0), owner, tm, &c).status(day(20, 18, 0), done)
	// Закрыта D, затем переназначена на C: учитывается у текущего исполнителя.
	t12 := e.taskAt(day(11, 0, 0), owner, tm, &d).status(day(12, 0, 0), done).assign(day(13, 0, 0), &c)
	// D: одно закрытие плюс задача, закрытая до окна, повторно открытая и снова закрытая в окне.
	t6 := e.taskAt(day(14, 0, 0), owner, tm, &d).status(day(15, 0, 0), done)
	t14 := e.taskAt(time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC), owner, tm, &d).
		status(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), done).status(day(1, 0, 0), todo).status(day(28, 0, 0), done)
	// Admin: одно закрытие — четвёртое место, в топ из трёх не попадает.
	t7 := e.taskAt(day(15, 0, 0), owner, tm, &admin).status(day(16, 0, 0), done)
	// Без исполнителя: в топ не входит, в среднее входит.
	t8 := e.taskAt(day(16, 0, 0), owner, tm, nil).status(day(17, 0, 0), done)
	// Бывший участник: три закрытия в окне, назначения сняты и членство удалено — в топе его нет.
	var goneTasks []*statsTask
	for i := 0; i < 3; i++ {
		goneTasks = append(goneTasks, e.taskAt(day(5, i, 0), owner, tm, &gone).status(day(6, i, 0), done).assign(day(7, i, 0), nil))
	}
	mustExecDB(t, e.db, `DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, platform.IDBytes(tm), platform.IDBytes(gone))
	// Открытые задачи.
	e.taskAt(day(20, 0, 0), owner, tm, &c).status(day(21, 0, 0), "in_progress")
	e.taskAt(day(22, 0, 0), owner, tm, nil)

	// Вторая команда с закрытиями и комментариями того же исполнителя не влияет на отчёт.
	other := e.teamWith(owner, map[uuid.UUID]team.Role{c: team.RoleMember})
	for i := 0; i < 4; i++ {
		e.taskAt(day(26, i, 0), owner, other, &c).status(day(27, i, 0), done).comments(1)
	}

	e.clock.Set(asOf.Add(123 * time.Nanosecond)) // as_of усекается до микросекунд, как DATETIME(6)
	rep, err := stats.Team(e.ctx, tm, owner)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AsOf.Equal(asOf) {
		t.Fatalf("as_of: %v", rep.AsOf)
	}
	if rep.ByStatus["todo"] != 2 || rep.ByStatus["in_progress"] != 1 || rep.ByStatus["done"] != 13 || len(rep.ByStatus) != 3 {
		t.Fatalf("по статусам: %v", rep.ByStatus)
	}

	// Топ: C — 3 (t3, t4, переназначенная t12); B и D по 2 — ничья по user_id; admin с 1 — четвёртый.
	first, second := b, d
	if bytes.Compare(d[:], b[:]) < 0 {
		first, second = d, b
	}
	want := []statistics.Assignee{{UserID: c, Name: "C", ClosedTasks: 3}, {UserID: first, ClosedTasks: 2}, {UserID: second, ClosedTasks: 2}}
	if len(rep.TopAssignees) != 3 {
		t.Fatalf("топ: %+v", rep.TopAssignees)
	}
	for i, w := range want {
		got := rep.TopAssignees[i]
		if got.UserID != w.UserID || got.ClosedTasks != w.ClosedTasks || got.Name == "" {
			t.Fatalf("топ[%d] = %+v, ожидалось %+v; весь топ %+v", i, got, w, rep.TopAssignees)
		}
	}

	// Среднее: все текущие done за всё время (включая закрытые до окна и без исполнителя), повторно
	// закрытая t14 — от исходного created_at до последнего closed_at; открытые задачи не входят.
	closed := []*statsTask{t1, t2, t3, t4, t5, t6, t7, t8, t12, t14}
	closed = append(closed, goneTasks...)
	var sum time.Duration
	for _, s := range closed {
		sum += s.t.ClosedAt.Sub(s.t.CreatedAt)
	}
	wantAvg := math.Round(float64(sum)/float64(len(closed))/float64(24*time.Hour)*1e6) / 1e6
	if rep.AvgCloseDays == nil || math.Abs(*rep.AvgCloseDays-wantAvg) > 1e-9 {
		t.Fatalf("среднее: %v, ожидалось %v", rep.AvgCloseDays, wantAvg)
	}
	if t14.t.CreatedAt.Month() != time.January || !t14.t.ClosedAt.Equal(day(28, 0, 0)) {
		t.Fatalf("t14: %+v", t14.t)
	}

	// Комментарии: 2 + 3 к задачам команды.
	if rep.CommentsTotal != 5 {
		t.Fatalf("комментарии: %d, ожидалось 5", rep.CommentsTotal)
	}

	// Права: admin видит отчёт, member — 403, бывший участник и посторонний — 404, как и чужая команда.
	if _, err := stats.Team(e.ctx, tm, admin); err != nil {
		t.Fatalf("admin: %v", err)
	}
	for name, c := range map[string]struct {
		team, user uuid.UUID
		want       error
	}{
		"member":          {tm, b, team.ErrForbidden},
		"бывший участник": {tm, gone, team.ErrNotFound},
		"чужой":           {tm, e.user("X"), team.ErrNotFound},
		"нет команды":     {mustID(t), owner, team.ErrNotFound},
	} {
		if _, err := stats.Team(e.ctx, c.team, c.user); !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Пустая команда: нули по статусам, пустые списки, comments_total 0 и среднее null.
func TestTeamReportEmpty(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	rep, err := statistics.NewService(mysqlstore.StatsStore{DB: e.db}, e.clock).Team(e.ctx, tm, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.ByStatus) != 3 || rep.ByStatus["todo"]+rep.ByStatus["in_progress"]+rep.ByStatus["done"] != 0 ||
		len(rep.TopAssignees) != 0 || rep.AvgCloseDays != nil || rep.CommentsTotal != 0 {
		t.Fatalf("пустой отчёт: %+v", rep)
	}
}

// Отчёт — ровно один SQL statement независимо от числа задач и исполнителей, и план использует
// индексы задач и комментариев, без полного сканирования таблиц.
func TestTeamReportSingleQueryAndPlan(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")
	members := map[uuid.UUID]team.Role{}
	var users []uuid.UUID
	for i := 0; i < 5; i++ {
		u := e.user("U")
		users = append(users, u)
		members[u] = team.RoleMember
	}
	tm := e.teamWith(owner, members)
	for i, u := range users {
		e.taskAt(day(10, i, 0), owner, tm, &u).status(day(11, i, 0), "done").comments(i)
	}
	e.clock.Set(day(31, 0, 0))

	// Один соединённый пул: счётчик Questions сессии видит все запросы отчёта.
	db := e.db
	conn, err := db.Conn(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	questions := func() int {
		var name string
		var n int
		if err := conn.QueryRowContext(e.ctx, `SHOW SESSION STATUS LIKE 'Questions'`).Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	asOf := e.clock.Now()
	query, args := mysqlstore.ReportQuery(tm, asOf.Add(-statistics.TopWindow), asOf)
	before := questions()
	rows, err := conn.QueryContext(e.ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		n++
	}
	rows.Close()
	// Разница: сам отчёт и второй SHOW STATUS.
	if got := questions() - before; got != 2 {
		t.Fatalf("отчёт выполнил %d запросов, ожидался 1", got-1)
	}
	if n != 1 { // все метрики — колонки одной строки, списки — JSON-массивы
		t.Fatalf("строк отчёта: %d", n)
	}

	plan, err := conn.QueryContext(e.ctx, `EXPLAIN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	cols, _ := plan.Columns()
	var scans []string
	for plan.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := plan.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := map[string]string{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			}
		}
		// ALL — полный проход таблицы, index — полный проход индекса: оба читали бы данные всех команд.
		if (row["table"] == "tasks" || row["table"] == "c" || row["table"] == "u") &&
			(row["key"] == "" || row["type"] == "ALL" || row["type"] == "index") {
			scans = append(scans, row["table"]+": type="+row["type"])
		}
	}
	sort.Strings(scans)
	if len(scans) > 0 {
		t.Fatalf("полное сканирование без индекса: %v", scans)
	}
}
