# D-Day

Statyczny landing page unconference **D-Day**.

- **Kiedy:** sobota 8 sierpnia 2026, 14:00–22:00
- **Gdzie:** Hakierspejs, Zielona 30 LU3, Łódź
- **Wstęp:** darmowy, zapisy obowiązkowe
- **Zapisy startują:** niedziela 26 lipca 2026, 15:00 (czasu warszawskiego) — na stronie odlicza countdown

## Rozwój

Pojedynczy plik `index.html`, bez zależności. Otwórz lokalnie w przeglądarce albo hostuj przez GitHub Pages.

Link do formularza zapisów podmień w `index.html` (szukaj `TODO` w sekcji `<script>` oraz `id="register"`).

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
