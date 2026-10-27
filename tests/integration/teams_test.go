//go:build integration

package integration

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

func TestTeamCreationIsAtomic(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")

	tm, err := e.teams.Create(e.ctx, owner, "Platform")
	if err != nil {
		t.Fatal(err)
	}
	if tm.Role != team.RoleOwner || tm.CreatedBy != owner {
		t.Fatalf("команда: %+v", tm)
	}
	id := platform.IDBytes(tm.ID)
	if e.count(`SELECT COUNT(*) FROM team_members WHERE team_id = ? AND user_id = ? AND role = 'owner'`, id, platform.IDBytes(owner)) != 1 {
		t.Fatal("нет членства owner")
	}
	if e.count(`SELECT COUNT(*) FROM team_members WHERE team_id = ?`, id) != 1 {
		t.Fatal("владелец должен быть единственным участником")
	}
	if e.revision(tm.ID) != 0 {
		t.Fatal("ревизия кеша создаётся вместе с командой со значением 0")
	}
}

// Ошибка вставки членства owner (настоящая ошибка MySQL из триггера) откатывает команду целиком.
func TestTeamCreationRollsBack(t *testing.T) {
	for _, table := range []string{"team_members", "team_task_cache_versions"} {
		t.Run(table, func(t *testing.T) {
			e := newEnv(t)
			owner := e.user("Owner")
			restore := testutil.FailInserts(t, e.cfg, table)

			if _, err := e.teams.Create(e.ctx, owner, "Broken"); err == nil {
				t.Fatal("ожидалась ошибка создания")
			}
			for _, q := range []string{`SELECT COUNT(*) FROM teams`, `SELECT COUNT(*) FROM team_members`, `SELECT COUNT(*) FROM team_task_cache_versions`} {
				if n := e.count(q); n != 0 {
					t.Errorf("%s = %d: частичная запись осталась", q, n)
				}
			}
			restore()
			if _, err := e.teams.Create(e.ctx, owner, "Works"); err != nil {
				t.Fatalf("после снятия отказа: %v", err)
			}
		})
	}
}

func TestTeamVisibilityAndInvites(t *testing.T) {
	e := newEnv(t)
	ann, bob, cat, dan := e.user("Ann"), e.user("Bob"), e.user("Cat"), e.user("Dan")

	teamA := e.teamWith(ann, map[uuid.UUID]team.Role{bob: team.RoleAdmin})
	teamB := e.teamWith(bob, nil)
	teamC := e.teamWith(cat, nil)

	// Bob видит обе свои команды со своими ролями, порядок id ASC, без третьей команды.
	got, err := e.teams.List(e.ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != teamA || got[1].ID != teamB || got[0].Role != team.RoleAdmin || got[1].Role != team.RoleOwner {
		t.Fatalf("команды Bob: %+v", got)
	}
	if got, _ := e.teams.List(e.ctx, dan); len(got) != 0 {
		t.Fatalf("Dan не состоит в командах: %+v", got)
	}

	// Состав — только участникам; чужой и несуществующей команде одинаково ErrNotFound.
	if _, err := e.teams.Members(e.ctx, teamC, bob); !errors.Is(err, team.ErrNotFound) {
		t.Fatalf("чужой состав: %v", err)
	}
	if _, err := e.teams.Members(e.ctx, mustID(t), bob); !errors.Is(err, team.ErrNotFound) {
		t.Fatalf("несуществующая команда: %v", err)
	}

	// Admin приглашает; приглашённый — member.
	m, err := e.teams.Invite(e.ctx, teamA, bob, dan)
	if err != nil || m.Role != team.RoleMember {
		t.Fatalf("приглашение admin: %+v %v", m, err)
	}
	members, err := e.teams.Members(e.ctx, teamA, dan)
	if err != nil || len(members) != 3 {
		t.Fatalf("состав: %+v %v", members, err)
	}
	for i := 1; i < len(members); i++ {
		if platform.IDString(members[i-1].UserID) >= platform.IDString(members[i].UserID) {
			t.Fatalf("порядок user_id ASC нарушен: %+v", members)
		}
	}

	// Ошибки приглашения не меняют состав.
	before := e.count(`SELECT COUNT(*) FROM team_members`)
	cases := map[string]struct {
		actor, team, target uuid.UUID
		want                error
	}{
		"member приглашает": {dan, teamA, cat, team.ErrForbidden},
		"дубль":             {ann, teamA, dan, team.ErrAlreadyMember},
		"себя":              {ann, teamA, ann, team.ErrAlreadyMember},
		"нет пользователя":  {ann, teamA, mustID(t), team.ErrNotFound},
		"неучастник":        {cat, teamA, cat, team.ErrNotFound},
		"несуществующая":    {ann, mustID(t), cat, team.ErrNotFound},
	}
	for name, c := range cases {
		if _, err := e.teams.Invite(e.ctx, c.team, c.actor, c.target); !errors.Is(err, c.want) {
			t.Errorf("%s: получено %v, ожидалось %v", name, err, c.want)
		}
	}
	if after := e.count(`SELECT COUNT(*) FROM team_members`); after != before {
		t.Fatalf("состав изменился после отказов: %d → %d", before, after)
	}
}

func TestChangeRoleOnlyByOwner(t *testing.T) {
	e := newEnv(t)
	owner, admin, member, outsider := e.user("O"), e.user("A"), e.user("M"), e.user("X")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{admin: team.RoleAdmin, member: team.RoleMember})

	cases := map[string]struct {
		actor, target uuid.UUID
		role          team.Role
		want          error
	}{
		"admin меняет member":  {admin, member, team.RoleAdmin, team.ErrForbidden},
		"admin меняет себя":    {admin, admin, team.RoleMember, team.ErrForbidden},
		"admin меняет owner":   {admin, owner, team.RoleMember, team.ErrForbidden},
		"member меняет":        {member, member, team.RoleAdmin, team.ErrForbidden},
		"owner меняет owner":   {owner, owner, team.RoleAdmin, team.ErrOwnerRoleImmutable},
		"цель не участник":     {owner, outsider, team.RoleAdmin, team.ErrNotFound},
		"отправитель неучастн": {outsider, member, team.RoleAdmin, team.ErrNotFound},
	}
	for name, c := range cases {
		if _, err := e.teams.ChangeRole(e.ctx, tm, c.actor, c.target, c.role); !errors.Is(err, c.want) {
			t.Errorf("%s: получено %v, ожидалось %v", name, err, c.want)
		}
	}
	var ve *platform.ValidationError
	if _, err := e.teams.ChangeRole(e.ctx, tm, owner, member, team.RoleOwner); !errors.As(err, &ve) {
		t.Errorf("выдача owner: %v", err)
	}

	// Owner повышает member до admin, затем понижает admin; повтор той же роли — успех без изменений.
	for _, step := range []struct {
		target uuid.UUID
		role   team.Role
	}{{member, team.RoleAdmin}, {admin, team.RoleMember}, {admin, team.RoleMember}} {
		if m, err := e.teams.ChangeRole(e.ctx, tm, owner, step.target, step.role); err != nil || m.Role != step.role {
			t.Fatalf("смена роли: %+v %v", m, err)
		}
	}
	// Новые полномочия действуют для следующих запросов: бывший admin больше не приглашает.
	if _, err := e.teams.Invite(e.ctx, tm, admin, outsider); !errors.Is(err, team.ErrForbidden) {
		t.Fatalf("понижённый admin: %v", err)
	}
	if _, err := e.teams.Invite(e.ctx, tm, member, outsider); err != nil {
		t.Fatalf("повышенный member: %v", err)
	}
}

// Протокол блокировок команды: смена роли держит строку команды FOR UPDATE, поэтому идущие
// одновременно запись задачи (FOR SHARE) и приглашение (FOR UPDATE) ждут её завершения и затем
// читают новую роль, а не устаревшую.
func TestRoleChangeDuringWriteUsesFreshRole(t *testing.T) {
	e := newEnv(t)
	owner, admin, member := e.user("O"), e.user("A"), e.user("M")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{admin: team.RoleAdmin, member: team.RoleMember})
	foreign := e.createTask(member, tm, "Задача участника", nil)
	newcomer := e.user("N")

	// Транзакция смены роли по тому же протоколу: команда FOR UPDATE, затем UPDATE роли. Не фиксируется,
	// пока конкуренты не окажутся в ожидании блокировки.
	tx, err := e.db.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var locked int
	if err := tx.QueryRow(`SELECT 1 FROM teams WHERE id = ? FOR UPDATE`, platform.IDBytes(tm)).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE team_members SET role = 'member' WHERE team_id = ? AND user_id = ?`,
		platform.IDBytes(tm), platform.IDBytes(admin)); err != nil {
		t.Fatal(err)
	}

	updateErr, inviteErr := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := e.tasks.Update(e.ctx, admin, foreign.ID, task.UpdateInput{Version: 1, Title: task.Set("Правка admin")})
		updateErr <- err
	}()
	go func() {
		_, err := e.teams.Invite(e.ctx, tm, admin, newcomer)
		inviteErr <- err
	}()
	testutil.WaitForLockWaits(t, e.cfg, 2)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := <-updateErr; !errors.Is(err, task.ErrForbidden) {
		t.Errorf("запись задачи должна увидеть новую роль member: %v", err)
	}
	if err := <-inviteErr; !errors.Is(err, team.ErrForbidden) {
		t.Errorf("приглашение должно увидеть новую роль member: %v", err)
	}
	if e.version(foreign.ID) != 1 || e.historyCount(foreign.ID) != 1 {
		t.Error("задача изменена по устаревшим правам")
	}
	if e.count(`SELECT COUNT(*) FROM team_members WHERE user_id = ?`, platform.IDBytes(newcomer)) != 0 {
		t.Error("приглашение выполнено по устаревшим правам")
	}

	// Обратный случай: повышение во время записи — запись ждёт и выполняется с новыми правами.
	tx2, err := e.db.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback() }()
	if err := tx2.QueryRow(`SELECT 1 FROM teams WHERE id = ? FOR UPDATE`, platform.IDBytes(tm)).Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec(`UPDATE team_members SET role = 'admin' WHERE team_id = ? AND user_id = ?`,
		platform.IDBytes(tm), platform.IDBytes(admin)); err != nil {
		t.Fatal(err)
	}
	go func() {
		_, err := e.tasks.Update(e.ctx, admin, foreign.ID, task.UpdateInput{Version: 1, Title: task.Set("Правка admin")})
		updateErr <- err
	}()
	testutil.WaitForLockWaits(t, e.cfg, 1)
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-updateErr; err != nil {
		t.Fatalf("после повышения запись должна пройти: %v", err)
	}
}
