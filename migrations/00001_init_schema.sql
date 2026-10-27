-- +goose Up
-- ID сущностей: UUIDv7, генерируются приложением, хранятся как BINARY(16) в стандартном порядке RFC.
-- AUTO_INCREMENT и SQL-генерация UUID не используются. Время: DATETIME(6) в UTC.

-- +goose StatementBegin
CREATE TABLE users (
    id            BINARY(16)   NOT NULL,
    email         VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    name          VARCHAR(120) NOT NULL,
    created_at    DATETIME(6)  NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_email (email),
    CONSTRAINT chk_users_name CHECK (CHAR_LENGTH(name) BETWEEN 1 AND 120)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE teams (
    id         BINARY(16)   NOT NULL,
    name       VARCHAR(120) NOT NULL,
    created_by BINARY(16)   NOT NULL,
    created_at DATETIME(6)  NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT fk_teams_created_by FOREIGN KEY (created_by) REFERENCES users (id),
    CONSTRAINT chk_teams_name CHECK (CHAR_LENGTH(name) BETWEEN 1 AND 120)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE team_members (
    team_id    BINARY(16)  NOT NULL,
    user_id    BINARY(16)  NOT NULL,
    role       VARCHAR(16) NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (team_id, user_id),
    KEY idx_team_members_user_team (user_id, team_id),
    CONSTRAINT fk_team_members_team FOREIGN KEY (team_id) REFERENCES teams (id),
    CONSTRAINT fk_team_members_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT chk_team_members_role CHECK (role IN ('owner', 'admin', 'member'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tasks (
    id          BINARY(16)   NOT NULL,
    title       VARCHAR(255) NOT NULL,
    description TEXT         NOT NULL,
    status      VARCHAR(16)  NOT NULL,
    created_by  BINARY(16)   NOT NULL,
    team_id     BINARY(16)   NOT NULL,
    assignee_id BINARY(16)   NULL,
    version     BIGINT       NOT NULL DEFAULT 1,
    created_at  DATETIME(6)  NOT NULL,
    updated_at  DATETIME(6)  NOT NULL,
    closed_at   DATETIME(6)  NULL,
    PRIMARY KEY (id),
    KEY idx_tasks_team_id (team_id, id),
    KEY idx_tasks_team_status_assignee (team_id, status, assignee_id, id),
    KEY idx_tasks_team_assignee (team_id, assignee_id, id),
    KEY idx_tasks_team_status_closed (team_id, status, closed_at, assignee_id),
    CONSTRAINT fk_tasks_team FOREIGN KEY (team_id) REFERENCES teams (id),
    CONSTRAINT fk_tasks_created_by FOREIGN KEY (created_by) REFERENCES users (id),
    CONSTRAINT fk_tasks_assignee FOREIGN KEY (assignee_id) REFERENCES users (id),
    CONSTRAINT chk_tasks_status CHECK (status IN ('todo', 'in_progress', 'done')),
    CONSTRAINT chk_tasks_version CHECK (version >= 1),
    CONSTRAINT chk_tasks_title CHECK (CHAR_LENGTH(title) BETWEEN 1 AND 255)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE task_history (
    id           BINARY(16)  NOT NULL,
    task_id      BINARY(16)  NOT NULL,
    changed_by   BINARY(16)  NOT NULL,
    task_version BIGINT      NOT NULL,
    changes      JSON        NOT NULL,
    created_at   DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_task_history_version (task_id, task_version),
    CONSTRAINT fk_task_history_task FOREIGN KEY (task_id) REFERENCES tasks (id),
    CONSTRAINT fk_task_history_user FOREIGN KEY (changed_by) REFERENCES users (id),
    CONSTRAINT chk_task_history_version CHECK (task_version >= 1)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE task_comments (
    id         BINARY(16)  NOT NULL,
    task_id    BINARY(16)  NOT NULL,
    user_id    BINARY(16)  NOT NULL,
    content    TEXT        NOT NULL,
    created_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_task_comments_task (task_id, id),
    CONSTRAINT fk_task_comments_task FOREIGN KEY (task_id) REFERENCES tasks (id),
    CONSTRAINT fk_task_comments_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- Служебная ревизия кеша списков задач: одна строка на команду, создаётся вместе с командой.
-- +goose StatementBegin
CREATE TABLE team_task_cache_versions (
    team_id  BINARY(16) NOT NULL,
    revision BIGINT     NOT NULL DEFAULT 0,
    PRIMARY KEY (team_id),
    CONSTRAINT fk_cache_versions_team FOREIGN KEY (team_id) REFERENCES teams (id),
    CONSTRAINT chk_cache_versions_revision CHECK (revision >= 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
-- +goose StatementEnd

-- +goose Down
-- ВНИМАНИЕ: откат удаляет все таблицы и данные. Используйте только для локального сброса.
DROP TABLE IF EXISTS team_task_cache_versions;
DROP TABLE IF EXISTS task_comments;
DROP TABLE IF EXISTS task_history;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS team_members;
DROP TABLE IF EXISTS teams;
DROP TABLE IF EXISTS users;
