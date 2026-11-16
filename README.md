# Druckwunsch

Sammelt Links zu 3D-Modellen (MakerWorld, Printables, Thingiverse, …) als Druckwünsche:
Listen pro Benutzer, Teilen (Lesen/Bearbeiten), Status (Neu → Zu drucken → In Druck → Gedruckt/Verworfen),
Labels, Kommentare, MakerWorld-Druckprofile mit Direktlink. Login per OIDC.

## Daten hereinholen
- **Lesezeichen „→ Druckwunsch“** (Einstellungen): auf der Modellseite klicken, übernimmt Titel, Bilder,
  Designer und bei MakerWorld alle Druckprofile. Läuft im Browser, daher kein Problem mit dem Bot-Schutz.
- **URL einfügen** (`/add`): Bei MakerWorld und Printables blockt der Bot-Schutz den Abruf durch den Server.
  Der Eintrag wird trotzdem angelegt, die Metadaten kommen später per Lesezeichen dazu.
- **Teilen vom Handy**: als PWA installieren (Android) oder iOS-Kurzbefehl auf `/share?url=…`.

## Konfiguration (Env)
| Variable | Bedeutung |
|---|---|
| `DW_BASE_URL` | öffentliche URL, z.B. `https://druckwunsch.example.com` |
| `DW_OIDC_ISSUER`, `DW_OIDC_CLIENT_ID`, `DW_OIDC_CLIENT_SECRET` | OIDC-Client; Redirect `<BASE>/auth/callback` |
| `DW_OIDC_NAME` | Text auf dem Login-Knopf |
| `DW_DATA_DIR` | SQLite-DB + Bild-Cache (Container: `/data`) |
| `DW_DEV_LOGIN=true` | nur lokal: Login ohne IdP |

## Entwickeln
```
docker run --rm -u 1000:1000 -e HOME=/tmp -v ~/.cache/dw-gomod:/go -v $PWD:/src -w /src golang:1.26-alpine go test ./...
docker build --network host -t druckwunsch:latest .
```
