package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

// DotEnvFile — файл переменных в рабочем каталоге процесса.
const DotEnvFile = ".env"

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Getenv возвращает getenv для Load: переменная окружения процесса приоритетнее значения из
// файла path, даже если она задана пустой. Отсутствующий файл не ошибка — тогда используется
// только окружение (так работает контейнер: .env в образ не попадает).
func Getenv(path string) (func(string) string, error) {
	file, err := readDotEnv(path)
	if err != nil {
		return nil, err
	}
	return func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		return file[key]
	}, nil
}

func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	vars, err := parseDotEnv(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return vars, nil
}

// parseDotEnv разбирает строки KEY=VALUE в формате Docker Compose: пустые строки и # —
// комментарии, необязательный префикс export, значение в "..." (с \n, \" и \\), в '...'
// (буквально) или без кавычек (до « #»). Подстановка ${VAR} не поддерживается.
// Значения в ошибки не попадают: в файле лежат секреты.
func parseDotEnv(r io.Reader) (map[string]string, error) {
	vars := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, raw, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKey.MatchString(key) {
			return nil, fmt.Errorf("строка %d: ожидалось KEY=VALUE", n)
		}
		value, err := dotEnvValue(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("строка %d (%s): %w", n, key, err)
		}
		vars[key] = value
	}
	return vars, sc.Err()
}

func dotEnvValue(raw string) (string, error) {
	switch {
	case strings.HasPrefix(raw, "'"):
		end := strings.Index(raw[1:], "'")
		if end < 0 {
			return "", errors.New("нет закрывающей кавычки")
		}
		return raw[1 : end+1], trailing(raw[end+2:])
	case strings.HasPrefix(raw, `"`):
		var b strings.Builder
		for i := 1; i < len(raw); i++ {
			switch c := raw[i]; {
			case c == '"':
				return b.String(), trailing(raw[i+1:])
			case c == '\\' && i+1 < len(raw):
				i++
				switch raw[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(raw[i])
				}
			default:
				b.WriteByte(c)
			}
		}
		return "", errors.New("нет закрывающей кавычки")
	default:
		if i := strings.Index(raw, " #"); i >= 0 {
			raw = raw[:i]
		}
		return strings.TrimSpace(raw), nil
	}
}

// trailing допускает после закрывающей кавычки только пробелы и комментарий.
func trailing(rest string) error {
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
		return errors.New("лишние символы после закрывающей кавычки")
	}
	return nil
}
