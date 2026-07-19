# D-Day

Strona i system zapisów na unconference **D-Day** w Hakierspejsie w Łodzi.

- **Kiedy:** sobota 8 sierpnia 2026, 14:00–22:00
- **Gdzie:** Hakierspejs, Zielona 30 LU3, Łódź
- **Wstęp:** darmowy, zapisy obowiązkowe (20 miejsc + 20 na liście rezerwowej)
- **Zapisy startują:** niedziela 26 lipca 2026, 15:00 czasu polskiego

Powyższe daty to wartości domyślne — wszystkie są konfigurowalne przez zmienne
środowiskowe, patrz [Zmiana terminu wydarzenia](#zmiana-terminu-wydarzenia).

## Architektura

Dwie binarki Go (bez CGO), wspólny pakiet z datami i konfiguracją terminu:

| Komponent | Ścieżka | Opis |
|---|---|---|
| Serwer WWW | `.` (`main.go`, `server.go`, `templates.go`) | landing + rejestracja; `index.html` i `privacy.html` są `go:embed`-owane w binarce |
| Bot Matrix | `cmd/bot`, `internal/matrixbot` | nasłuchuje `!register`, wysyła DM z jednorazowym linkiem |
| Bramka czasowa i daty | `internal/regwindow` | jedyne źródło prawdy dla terminów; generuje polskie opisy dat |
| Baza | `internal/store` | SQLite przez `modernc.org/sqlite` (czysty Go) |

Trasy serwera WWW:

| Trasa | Opis |
|---|---|
| `GET /` | landing (`index.html`); tylko dokładnie `/`, żadnego przeglądania katalogu |
| `GET /register?t=…` | formularz zapisu dla ważnego tokenu |
| `POST /register` | zapis (miejscowość + e-mail); tożsamość wyłącznie z tokenu |
| `GET /api/count` | JSON: obłożenie miejsc + wszystkie daty i ich opisy (patrz niżej) |
| `GET /api/registered?h=@user:hs` | wewnętrzne, dla bota; wymaga `Authorization: Bearer $INTERNAL_TOKEN`, bez tokenu 404 |
| `GET /privacy` | polityka prywatności (`privacy.html`) |
| `GET /healthz` | health check (używany też przez `dday -healthcheck`) |

## Jak działa rejestracja

1. Użytkownik pisze `!register` do bota (DM albo pokój z allowlisty).
2. Bot sprawdza bramkę czasową i — jeśli ma `INTERNAL_TOKEN` — pyta
   `/api/registered`, czy ten handle już się zapisał.
3. Bot generuje token: `{handle, issued}` zaszyfrowane kluczem age (odbiorcą jest
   klucz publiczny serwera), podpisane HMAC-em (`TOKEN_SECRET`), całość base64.
4. Bot wysyła w DM link `REGISTER_URL?t=<token>`.
5. `GET /register?t=…` odszyfrowuje i weryfikuje token: zły podpis → „Nieprawidłowy
   link", starszy niż **48 h** (lub wystawiony w przyszłości ponad 5 min zapasu na
   zegar) → „Link wygasł".
6. Formularz zbiera miejscowość i e-mail; `POST` zapisuje rekord w SQLite i
   przydziela numer uczestnika (`AUTOINCREMENT`).
7. Numery 1–20 to miejsca potwierdzone, 21–40 lista rezerwowa, powyżej — brak miejsc.
8. Link jest jednorazowy: po zapisie ten sam link pokazuje potwierdzenie, a kolejny
   `!register` jest odmawiany (`/api/registered`) — próba ponownego `POST`
   kończy się stroną „Już zapisany", bez drugiego rekordu.

Landing pobiera stan z `/api/count` (licznik zapisanych, pasek, lista rezerwowa,
flaga `open` i daty). Bez API (np. GitHub Pages) strona degraduje się do wartości
wpisanych na sztywno w `index.html`.

## Konfiguracja

Serwer WWW:

| Zmienna | Domyślnie | Opis |
|---|---|---|
| `PORT` | `3329` | port nasłuchu |
| `STATIC_DIR` | *(puste)* | katalog z `index.html`/`privacy.html` zamiast wersji wbudowanej (dev) |
| `DB_PATH` | `./dday.db` | plik bazy SQLite |
| `AGE_KEY` | `config/dday_ed25519` | ścieżka klucza prywatnego age |
| `AGE_KEY_DATA` | *(puste)* | ten sam klucz przekazany base64 (ma pierwszeństwo; używane w kontenerze) |
| `TOKEN_SECRET` | *(puste)* | wspólny sekret HMAC dla tokenów; musi być identyczny u bota |
| `INTERNAL_TOKEN` | *(puste)* | bearer chroniący `/api/registered`; puste = endpoint wyłączony (404) |
| `REGISTRATION_OPEN` | *(puste)* | `1`/`true`/`yes` wymusza otwarte zapisy; inaczej decyduje bramka czasowa |
| `REGISTRATION_OPEN_AT` | `2026-07-26 15:00` | moment otwarcia zapisów |
| `EVENT_START_AT` | `2026-08-08 14:00` | początek wydarzenia |
| `EVENT_END_AT` | `2026-08-08 22:00` | koniec wydarzenia |

Bot Matrix (`cmd/bot`, konfiguracja zwykle w `matrix.env`):

| Zmienna | Domyślnie | Opis |
|---|---|---|
| `MATRIX_HOMESERVER` | **wymagane** | np. `https://matrix.org` |
| `MATRIX_USER` | **wymagane** | np. `@ddaybot:matrix.org` |
| `MATRIX_PASSWORD` | **wymagane** | hasło bota |
| `MATRIX_ROOM` | *(puste)* | pokój używany przez skrypty `make matrix-hello` / `matrix-send` |
| `REGISTER_URL` | `https://dday.hs-ldz.pl/` | baza linku rejestracyjnego (jej origin służy też do `/api/registered`) |
| `AGE_PUB` | `config/dday_ed25519.pub` | klucz publiczny age, którym bot szyfruje tokeny |
| `AGE_PUB_DATA` | *(puste)* | ten sam klucz base64 (ma pierwszeństwo) |
| `TOKEN_SECRET` | *(puste)* | jak wyżej — musi zgadzać się z serwerem |
| `INTERNAL_TOKEN` | *(puste)* | włącza pytanie `/api/registered` przed wydaniem linku |
| `ALLOWED_ROOMS` | *(puste)* | lista room id po przecinku; puste = bot reaguje wszędzie |
| `DM_CACHE_PATH` | *(puste)* | plik cache `handle → room id`, przeżywa restart |
| `REGISTRATION_OPEN`, `REGISTRATION_OPEN_AT` | jak wyżej | bot używa tej samej bramki i tego samego opisu daty |

Formaty dat (`*_AT`): RFC3339 (`2026-07-26T15:00:00+02:00`) albo
`2006-01-02 15:04` / `2006-01-02 15:04:05` / `2006-01-02` interpretowane w strefie
Europe/Warsaw. Wartość niepoprawna → wpis w logu i użycie domyślnej (bez crasha).

## Uruchomienie lokalnie

```sh
make keys       # jednorazowo: para kluczy age do config/ (gitignored)
make run        # serwer WWW na :3329 (index.html z binarki)
make dev        # to samo, ale index.html czytany z dysku (live edit)
make bot        # bot Matrix, konfiguracja z ./matrix.env
make check      # walidacja matrix.env
make test       # go test -race ./...
```

Sekrety trzymamy poza repo: `matrix.env` (wzór w `matrix.env.example`), `.env`
generowany przez `make up` i katalog `config/` — wszystkie są w `.gitignore`.

Podgląd z niestandardowymi datami:

```sh
REGISTRATION_OPEN_AT="2026-09-01 12:00" \
EVENT_START_AT="2026-09-05 10:00" EVENT_END_AT="2026-09-05 18:00" make run
```

## Deployment

```sh
make up     # generuje .env i odpala docker compose up -d --build
make logs   # docker compose logs -f
make down   # zatrzymanie stacku
```

`make up` sprawdza obecność kluczy age i `matrix.env`, a następnie zapisuje `.env`
z: `AGE_KEY_DATA`, `AGE_PUB_DATA` (klucze base64 — plik 0600 byłby nieczytelny dla
użytkownika `nonroot` w obrazie distroless), `INTERNAL_TOKEN` i `TOKEN_SECRET`
(losowane raz i potem reużywane) oraz `REGISTRATION_OPEN` (domyślnie `1`).

`docker-compose.yml` wystawia serwis `dday` przez Traefika na
`dday.hs-ldz.pl` (entrypoint `websecure`, certresolver `myresolver`) i uruchamia
serwis `bot`. Dane trwałe: wolumen `dday-data` (`/data/dday.db`) i `bot-data`
(cache DM-ów bota). Zmienne `REGISTRATION_OPEN_AT` / `EVENT_START_AT` /
`EVENT_END_AT` są przepuszczane do obu serwisów — ustaw je w `.env`, jeśli
zmieniasz termin.

## Zmiana terminu wydarzenia

Terminy pochodzą wyłącznie z `internal/regwindow`: serwer używa ich na stronach
„zapisy nieotwarte", bot w odpowiedzi „jeszcze nie wystartowały", a landing
pobiera je z `/api/count` (`openAt`, `eventStartAt`, `eventEndAt` + gotowe polskie
teksty `openText`, `openHowto`, `openShort`, `openShortTime`, `eventText`,
`eventShort`, `eventShortTime`, `eventBadge`). Polskie odmiany („niedziela”,
„lipca”, „w niedzielę”) generuje sam pakiet — nic nie synchronizuje się ręcznie.

Żeby przesunąć termin, ustaw w `.env` (deployment) albo w środowisku procesu:

```sh
REGISTRATION_OPEN_AT="2026-09-01 12:00"
EVENT_START_AT="2026-09-05 10:00"
EVENT_END_AT="2026-09-05 18:00"
```

Restart kontenerów i tyle — landing, formularz i bot mówią to samo.

Ręcznie trzeba poprawić tylko rzeczy, których nie da się wyliczyć w runtime:

- `index.html`: `<meta name="description">` i `<meta property="og:description">` —
  statyczne, bo crawlery nie wykonują naszego JS.
- `index.html`: literały fallbackowe (`REG_OPEN`, `EVENT`, teksty kafelków) —
  używane wyłącznie, gdy strona jest serwowana bez API (np. GitHub Pages). Warto
  je odświeżyć przy zmianie terminu, choć produkcja bierze daty z API.
- Nagłówek tego README.
- `privacy.html` — pole „Ostatnia aktualizacja”, jeśli zmieniacie samą politykę.

## RODO — usuwanie danych uczestnika

Na żądanie uczestnika (kontakt: kontakt@hakierspejs.pl) usuwamy jego rekord
z bazy zapisów. Służy do tego wbudowany tryb CLI binarki, wskazujący uczestnika
po identyfikatorze Matrix (`matrix_handle`):

```sh
# w kontenerze (usługa dday używa /data/dday.db z wolumenu):
docker compose run --rm dday -delete @user:homeserver

# lub bezpośrednio przy uruchomionej binarce (DB_PATH wskazuje bazę):
DB_PATH=./dday.db dday -delete @user:homeserver
```

Komenda wypisuje wynik i kończy się kodem `0`, gdy rekord został usunięty, albo
`1`, gdy takiego zapisu nie było (lub wystąpił błąd). Uwaga: numer uczestnika to
`AUTOINCREMENT` — po usunięciu **nie jest odtwarzany** i nie zostaje przydzielony
ponownie. To celowe i akceptowalne.

## Testy

```sh
make test          # go test -race ./...
make fmt vet       # gofmt -w . && go vet ./...
```

Pokrycie: pełny przepływ rejestracji (token → formularz → zapis → duplikat →
lista rezerwowa → komplet), TTL i manipulacja tokenem, bramka czasowa i parsowanie
`*_AT`, generowanie polskich opisów dat, pola `/api/count`, nagłówki
bezpieczeństwa, tryb `-delete`, ładowanie kluczy oraz logika bota (allowlista
pokojów, cache DM-ów, odpowiedzi przed otwarciem zapisów).

CI (GitHub Actions, `.github/workflows`) uruchamia `gofmt -l`, `go vet`,
`go build`, `go test -race` oraz build obrazu Dockera.
