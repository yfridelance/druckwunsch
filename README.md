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
| `DW_GROUP_PRINTER` | IdP-Gruppe der Drucker (Standard `druckwunsch_full`) |
| `DW_GROUP_USER` | IdP-Gruppe der Wünscher (Standard `druckwunsch_limited`); wer in keiner Gruppe ist, kommt nicht rein |
| `DW_OIDC_GROUPS_CLAIM` | Name des Gruppen-Claims (Standard `groups`, Scope `groups` wird angefragt) |
| `DW_DATA_DIR` | SQLite-DB + Bild-Cache (Container: `/data`) |
| `DW_DEV_LOGIN=true` | nur lokal: Login ohne IdP |

## Entwickeln
```
docker run --rm -u 1000:1000 -e HOME=/tmp -v ~/.cache/dw-gomod:/go -v $PWD:/src -w /src golang:1.26-alpine go test ./...
docker build --network host -t druckwunsch:latest .
```

## Rollen

- **Drucker** (`DW_GROUP_PRINTER`): sieht alle freigegebenen Wünsche, führt sie durch den Status (Neu → Zu drucken → In Druck → Gedruckt), verwaltet Labels und kann Wünsche im Namen anderer erfassen (User oder Freitext-Name).
- **Wünscher** (`DW_GROUP_USER`): erfasst eigene Wünsche. Sie bleiben **Entwurf**, bis sie freigegeben werden (einzeln oder „Alle freigeben“). Solange ein Wunsch noch „Neu“ ist, kann er zurückgezogen werden.
- „Wunsch von“ ist der angemeldete User. Wer einen Wunsch für sich erfasst bekommt, sieht ihn und kann kommentieren.
- Die Rolle wird bei jedem Login aus den Gruppen übernommen.
