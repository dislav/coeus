# Самохостинг Coeus на Mac Mini

Инструкция по развёртыванию Coeus на Mac Mini в качестве постоянного сервера:
сборка Docker-образа, запуск с PostgreSQL+pgvector, подключение домена и доступ
извне по HTTPS.

Coeus — **один бинарник**: HTTP-сервер и пул воркеров работают в одном процессе.
Ему нужна только внешняя PostgreSQL с расширением `vector` (pgvector). Миграции
БД применяются автоматически при старте — отдельного шага нет.

---

## Содержание

- [Архитектура](#архитектура)
- [Требования](#требования)
- [Особенность Apple Silicon](#особенность-apple-silicon)
- [Быстрый старт](#быстрый-старт)
- [Доступ извне](#доступ-извне)
  - [Вариант A — белый IP + Caddy](#вариант-a--белый-ip--caddy)
  - [Вариант B — Cloudflare Tunnel](#вариант-b--cloudflare-tunnel-без-проброса-портов)
- [Настройка Mac Mini](#настройка-mac-mini)
- [Автозапуск после перезагрузки](#автозапуск-после-перезагрузки)
- [Чек-лист продакшна](#чек-лист-продакшна)
- [Бэкапы](#бэкапы)
- [Обслуживание](#обслуживание)
- [Диагностика](#диагностика)

---

## Архитектура

```
 Интернет → роутер (проброс 80/443) → Mac Mini
                                          ├─ Caddy      :80/:443   TLS, домен
                                          ├─ Coeus      :8080      Docker (HTTP + workers)
                                          └─ Postgres   :5432      pgvector, Docker (внутренняя сеть)
```

Три контейнера в одной Docker-сети:

| Контейнер  | Образ                  | Назначение                          |
|------------|------------------------|-------------------------------------|
| `db`       | `pgvector/pgvector:pg16` | PostgreSQL + pgvector             |
| `coeus`    | `coeus` (собирается)   | Приложение (HTTP + worker pool)    |
| `caddy`    | `caddy:2`              | Reverse-proxy, авто-HTTPS (Let's Encrypt) |

`coeus` обращается к БД по имени `db` внутри compose-сети. Порт `8080` наружу
не торчит — трафик идёт через Caddy.

---

## Требования

- **Mac Mini** (любой, Intel или Apple Silicon) с macOS 13+
- **Docker Desktop** (или OrbStack / Colima) с поддержкой compose
- **~2 ГБ RAM** минимум под контейнеры (воркеры + БД + libvips)
- **Доступ к интернету** — Coeus ходит в AI API (Kimi/DeepSeek/OpenAI)
- **Домен** — A-запись которого вы направите на Mac Mini
- Учётные данные AI:
  - `COEUS_AI_VISION_API_KEY` (Kimi/Moonshot) — **обязательно**
  - `COEUS_AI_REVIEWER_API_KEY` (DeepSeek) — **обязательно**
  - `COEUS_AI_EMBEDDER_API_KEY` (OpenAI-совместимый) — опционально; без него
    семантическая дедупликация пропускается (exact-hash дедуп остаётся)

---

## Особенность Apple Silicon

`Dockerfile` использует `golang:1.26-bookworm` и `debian:bookworm-slim` — оба
поддерживают `linux/arm64`. Docker Desktop на M-серии по умолчанию собирает
**нативно под arm64**, без эмуляции — сборка идёт быстро, ничего дополнительно
настраивать не нужно.

Если планируете запускать тот же образ на x86-сервере, соберите мульти-арх:
```bash
docker buildx build --platform linux/amd64,linux/arm64 -t coeus .
```

---

## Быстрый старт

### 1. Подготовьте проект

```bash
git clone <repo-url> coeus
cd coeus
```

### 2. Сгенерируйте секреты

```bash
openssl rand -hex 32          # → JWT_SECRET
openssl rand -hex 16          # → пароль PostgreSQL
```

### 3. Создайте `docker-compose.yml`

Положите рядом с `Dockerfile` (в корне репозитория):

```yaml
# docker-compose.yml
services:
  db:
    image: pgvector/pgvector:pg16
    restart: unless-stopped
    environment:
      POSTGRES_DB: coeus
      POSTGRES_PASSWORD: ЗАМЕНИТЕ_НА_СВОЙ_ПАРОЛЬ
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres -d coeus"]
      interval: 10s
      timeout: 3s
      retries: 5
    # порт наружу не торчим — доступ только из compose-сети

  coeus:
    image: coeus
    build: .
    restart: unless-stopped
    depends_on:
      db:
        condition: service_healthy
    environment:
      COEUS_POSTGRES_DSN: "postgres://postgres:ЗАМЕНИТЕ_НА_СВОЙ_ПАРОЛЬ@db:5432/coeus?sslmode=disable"
      COEUS_JWT_SECRET: "ЗАМЕНИТЕ_НА_СГЕНЕРИРОВАННЫЙ_JWT_СЕКРЕТ"
      COEUS_AI_VISION_API_KEY: "sk-..."
      COEUS_AI_VISION_BASE_URL: "https://api.moonshot.cn/v1"
      COEUS_AI_REVIEWER_API_KEY: "sk-..."
      COEUS_AI_REVIEWER_BASE_URL: "https://api.deepseek.com/v1"
      COEUS_AI_EMBEDDER_API_KEY: "sk-..."        # опционально
      COEUS_WORKERS_COUNT: "4"                    # под число ядер Mac Mini
    # 8080 торчит только в сеть compose — Caddy проксирует
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:8080/healthz"]
      interval: 10s
      timeout: 3s
      retries: 3

  caddy:
    image: caddy:2
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on: [coeus]

volumes:
  pgdata:
  caddy_data:
  caddy_config:
```

> Если выбрали [Cloudflare Tunnel](#вариант-b--cloudflare-tunnel-без-проброса-портов),
> сервис `caddy` не нужен — туннель проксирует напрямую в `coeus:8080`.

### 4. Создайте `Caddyfile`

```caddy
# Caddyfile
coeus.example.ru {
    reverse_proxy coeus:8080
}
```

Caddy сам получит и продлит сертификат Let's Encrypt. Никаких ручных certbot.

### 5. Соберите и запустите

```bash
docker compose build
docker compose up -d
docker compose ps
```

### 6. Проверьте локально

```bash
curl http://localhost:8080/healthz   # {"status":"ok"}
curl http://localhost:8080/readyz    # {"status":"ready"}  (пингует БД)
docker compose logs -f coeus         # логи приложения (slog JSON)
```

Готово. Осталось пустить трафик извне.

---

## Доступ извне

Это отдельная задача, и здесь 4 независимых грабли. Любой из них ломает всё.

### Сначала — проверьте тип IP

```bash
curl ifconfig.me            # ваш внешний IP
```

- Если IP **белый** (статический или динамический, но не за CGNAT) — подходит
  [Вариант A](#вариант-a--белый-ip--caddy) (Caddy + проброс портов).
- Если IP из диапазона `100.64.0.0/10` (CGNAT) или провайдер не даёт белый IP —
  используйте [Вариант B](#вариант-b--cloudflare-tunnel-без-проброса-портов)
  (Cloudflare Tunnel).

---

### Вариант A — белый IP + Caddy

1. **Статичный локальный IP для Mac Mini.** В настройках роутера закрепите
   DHCP-резервацию (например `192.168.1.50`), иначе после перезагрузки проброс
   портов уедет мимо.

2. **Проброс портов на роутере** (Port Forwarding):
   - `80/TCP`  → `192.168.1.50:80`
   - `443/TCP` → `192.168.1.50:443`

   > Не пробрасывайте `8080` напрямую без TLS — JWT-токены уйдут в открытом виде.
   > Caddy обязателен.

3. **DNS.**
   - Статический IP: A-запись `coeus.example.ru → <публичный-IP>` у регистратора.
   - Динамический IP: DDNS (Cloudflare API, noip, ddclient) — запись
     автообновляется.

4. **Фаервол macOS:** System Settings → Network → Firewall — разрешить входящие
   для Docker / Caddy (Docker Desktop обычно добавляет правила сам).

5. Проверьте снаружи (с телефона, отключив Wi-Fi):
   ```bash
   curl https://coeus.example.ru/healthz   # {"status":"ok"}
   ```

---

### Вариант B — Cloudflare Tunnel (без проброса портов)

Если заморачиваться с белым IP / CGNAT / пробросом не хочется — **рекомендуемый
путь для домашнего сервера**:

- не нужны пробросы портов на роутере;
- работает за любым NAT/CGNAT;
- TLS и защита от DDoS — на стороне Cloudflare;
- домен направляете на nameservers Cloudflare, туннель сам связывает
  `coeus.example.ru` с локальным `coeus:8080`.

**Настройка:**

1. Перенесите NS домена на Cloudflare.
2. В Cloudflare Zero Trust → Networks → Tunnels → **Create a tunnel**
   (тип Cloudflared). Запишите tunnel ID и токен.
3. Запустите `cloudflared` рядом (добавьте в `docker-compose.yml`):

   ```yaml
   cloudflared:
     image: cloudflare/cloudflared:latest
     restart: unless-stopped
     command: tunnel --no-autoupdate run
     environment:
       TUNNEL_TOKEN: "ЗАМЕНИТЕ_НА_ТОКЕН_ИЗ_DASHBOARD"
   ```

4. В dashboard создайте Public Hostname:
   - Subdomain/Domain: `coeus.example.ru`
   - Service: `http://coeus:8080`

5. Сервис `caddy` из compose можно убрать — туннель терминирует TLS сам.

```bash
docker compose up -d
curl https://coeus.example.ru/healthz   # {"status":"ok"}
```

---

## Настройка Mac Mini

Эти шаги обязательны для любого варианта — иначе ночью сервер «умрёт».

### Не давать засыпать

- **System Settings → Energy →** включить *«Prevent automatic sleeping when the
  display is off»*.
- Или временно из терминала: `caffeinate -s` (держит до закрытия окна).

### Статичный локальный IP

См. шаг 1 в [Варианте A](#вариант-a--белый-ip--caddy). Обязательно и для
Cloudflare Tunnel (чтобы Mac Mini был предсказуем в локальной сети).

### Фаервол

System Settings → Network → Firewall. Разрешить входящие соединения Docker.
При первом запуске Caddy macOS спросит — нажать *Allow*.

### Диск и место

`pgdata` и `caddy_data` растут медленно, но `docker compose` со временем
накапливает слои. Периодически: `docker system prune -f` (не трогает volumes).

---

## Автозапуск после перезагрузки

1. **Docker Desktop** → Settings → *«Start Docker Desktop when you sign in to
   your computer»*. Без этого после ребута Mac Mini контейнеры не поднимутся.

2. Включить **auto-login** для учётки, под которой стоит Docker (иначе Docker
   Desktop не запустится до входа пользователя). System Settings → Users &
   Groups → Auto-login.

3. `restart: unless-stopped` в compose поднимет контейнеры автоматически, как
   только стартует Docker-движок.

4. (Опц.) Если Mac Mini «безголовый» и без авто-логина — поставьте OrbStack или
   настройте `launchd`-плитку, запускающую `docker compose up -d` после загрузки.

---

## Чек-лист продакшна

- [ ] JWT-секрет — `openssl rand -hex 32` (не короткий пароль)
- [ ] Пароль PostgreSQL — случайный, не `dev`
- [ ] `pgdata` на volume (данные переживут пересоздание контейнера)
- [ ] `COEUS_POSTGRES_DSN` указывает на `db:5432` (внутри compose-сети)
- [ ] API-ключи AI с достаточными лимитами
- [ ] `COEUS_WORKERS_COUNT` ≈ число ядер Mac Mini (дефолт 4)
- [ ] Caddy / Cloudflare Tunnel спереди для TLS
- [ ] Mac Mini не засыпает, статичный локальный IP
- [ ] Docker Desktop автозапускается при входе
- [ ] Настроен бэкап `pgdata`

---

## Бэкапы

Volume `pgdata` — единственное, что нельзя потерять. Бэкап логического дампа
(не зависит от версии Postgres / pgvector):

```bash
# Разовый дамп
docker compose exec db pg_dump -U postgres coeus | gzip > backup_$(date +%F).sql.gz

# Восстановление
gunzip -c backup_2026-06-23.sql.gz | docker compose exec -T db psql -U postgres coeus
```

Регулярный бэкап по cron на Mac Mini (например в 03:00, копирование в iCloud /
S3 / любой объектное хранилище):

```cron
0 3 * * * cd /Users/you/coeus && docker compose exec -T db pg_dump -U postgres coeus | gzip > /Volumes/Backup/coeus_$(date +\%F).sql.gz && find /Volumes/Backup -name 'coeus_*.sql.gz' -mtime +14 -delete
```

> Физическое копирование volume (`docker run --rm -v pgdata:/d ... tar`) тоже
> работает, но логический `pg_dump` портабельнее при апгрейде Postgres.

---

## Обслуживание

### Обновить образ после изменений в коде

```bash
git pull
docker compose build
docker compose up -d        # перезапустит только coeus, БД не тронет
```

### Логи

```bash
docker compose logs -f coeus        # приложение (slog JSON)
docker compose logs -f caddy        # access-логи reverse-proxy
docker compose logs --since 10m     # все сервисы за 10 минут
```

### Перезапуск / остановка

```bash
docker compose restart coeus
docker compose down                  # остановить, данные в volumes сохранятся
docker compose down -v               # ВНИМАНИЕ: удалит pgdata и caddy_data
```

### Следить за здоровьем

- `/healthz` — приложение живо.
- `/readyz` — БД доступна.
- `docker compose ps` — статусы контейнеров (healthcheck встроен в compose).

---

## Диагностика

| Симптом | С чего начать |
|---|---|
| `curl ifconfig.me` отдаёт `100.64.x.x` | Вы за CGNAT — используйте Cloudflare Tunnel (Вариант B) |
| Домен не открывается снаружи, локально работает | Проверить проброс портов на роутере + фаервол macOS + A-запись DNS |
| `coeus` падает с `postgres.dsn is required` | Не подставились env в compose, проверьте отступы и кавычки |
| `coeus` падает с ошибкой миграции `vector` | БД поднялась из образа не `pgvector/pgvector:pg16` — проверьте сервис `db` |
| `readyz` отдаёт ошибку, `healthz` ок | БД недоступна из `coeus`: проверьте `depends_on` healthcheck и DSN |
| Изображение «не обрабатывается» | Воркеры/БД: `docker compose logs coeus`; см. reaper и `LISTEN/NOTIFY jobs_new` |
| Caddy не получает сертификат | Проверьте, что `80` доступен извне (Let's Encrypt HTTP-01) или используйте Cloudflare Tunnel |
| После ребута Mac Mini всё лежит | Включить автозапуск Docker Desktop + авто-логин пользователя |

---

## Ссылки

- Основной README репозитория — `/README.md` (production checklist, env-переменные)
- `AGENTS.md` — архитектура, async job model, команда сборки
- `Dockerfile` — multi-stage сборка (golang:1.26 → debian:bookworm-slim + libvips42)
- `internal/config/config.go` — полный список `COEUS_*` env-переменных
