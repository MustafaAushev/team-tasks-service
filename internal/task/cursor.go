package task

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// Виды списков, к которым привязан курсор.
const (
	kindTasks    = "tasks"
	kindHistory  = "task_history"
	kindComments = "task_comments"
)

// cursor — позиция в списке и привязка к выборке. Сервер курсоры не хранит: клиент получает
// next_cursor и передаёт его целиком, а позиция восстанавливается из проверенного токена.
type cursor struct {
	Kind     string `json:"k"`
	TeamID   string `json:"team"`
	TaskID   string `json:"task,omitempty"`
	Status   string `json:"status,omitempty"`
	Assignee string `json:"assignee,omitempty"`
	LastID   string `json:"last_id,omitempty"`
	LastVer  int64  `json:"last_version,omitempty"`
}

// cursorCodec подписывает курсоры HMAC-SHA256 отдельным секретом: base64url(JSON).base64url(MAC).
type cursorCodec struct{ secret []byte }

func (c cursorCodec) encode(p cursor) string {
	raw, _ := json.Marshal(p) // структура из строк и чисел сериализуется всегда
	body := base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + base64.RawURLEncoding.EncodeToString(c.mac(body))
}

// decode проверяет подпись и то, что курсор выдан для той же выборки (вид, команда, задача,
// фильтры); любое расхождение — ErrInvalidCursor без подробностей.
func (c cursorCodec) decode(token string, want cursor) (cursor, error) {
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return cursor{}, ErrInvalidCursor
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, c.mac(body)) {
		return cursor{}, ErrInvalidCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return cursor{}, ErrInvalidCursor
	}
	var p cursor
	if err := json.Unmarshal(raw, &p); err != nil {
		return cursor{}, ErrInvalidCursor
	}
	if p.Kind != want.Kind || p.TeamID != want.TeamID || p.TaskID != want.TaskID ||
		p.Status != want.Status || p.Assignee != want.Assignee {
		return cursor{}, ErrInvalidCursor
	}
	return p, nil
}

func (c cursorCodec) mac(body string) []byte {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte(body))
	return m.Sum(nil)
}
