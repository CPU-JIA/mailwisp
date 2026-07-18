#!/bin/sh
set -eu

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"

owner_password_file=${MAILWISP_POSTGRES_OWNER_PASSWORD_FILE:-/run/secrets/postgres_owner_password}
app_password_file=${MAILWISP_POSTGRES_APP_PASSWORD_FILE:-/run/secrets/postgres_app_password}
for password_file in "$owner_password_file" "$app_password_file"; do
    if [ ! -r "$password_file" ]; then
        echo "PostgreSQL password secret is not readable: $password_file" >&2
        exit 64
    fi
done

PGPASSWORD=$(cat "$owner_password_file")
app_password=$(cat "$app_password_file")
if [ -z "$PGPASSWORD" ] || [ -z "$app_password" ]; then
    echo "PostgreSQL password secrets must not be empty" >&2
    exit 64
fi
export PGPASSWORD

psql --set=ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    --set=database_name="$POSTGRES_DB" --set=app_password="$app_password" <<'SQL'
SELECT format(
    'CREATE ROLE mailwisp_app LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS',
    :'app_password'
)
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'mailwisp_app')
\gexec

ALTER ROLE mailwisp_app
    LOGIN
    PASSWORD :'app_password'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOINHERIT
    NOREPLICATION
    NOBYPASSRLS;
REVOKE ALL ON DATABASE :"database_name" FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE :"database_name" TO mailwisp_app;
GRANT USAGE ON SCHEMA public TO mailwisp_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO mailwisp_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO mailwisp_app;
ALTER DEFAULT PRIVILEGES FOR ROLE mailwisp_owner IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO mailwisp_app;
ALTER DEFAULT PRIVILEGES FOR ROLE mailwisp_owner IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO mailwisp_app;
SQL
