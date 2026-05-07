# gdrive-downloader

Desktop GUI app (Go + [Gio](https://gioui.org)) that downloads every file from
your Google Drive into a local folder, mirroring the Drive folder structure.

- OAuth scope (`drive`) — used to scan your files and optionally delete them from Drive after a verified download.
- Supports downloading files flagged by Google as malware/spam (acknowledges risk).
- Google-native files are exported to Office formats:
  Docs → `.docx`, Sheets → `.xlsx`, Slides → `.pptx`, Drawings → `.png`,
  Apps Script → `.json`.
- Resumable: a `.gdrive-state.json` next to the output skips files already
  pulled. Re-running picks up where it stopped and re-downloads anything whose
  `md5Checksum` / `modifiedTime` changed in Drive.
- Concurrent worker pool (up to 6) with retry/backoff on 429/5xx.

## Build

```
go build -o gdrive-downloader ./...
```

## Get a `credentials.json`

1. Open <https://console.cloud.google.com/>, create or pick a project.
2. **APIs & Services → Library** → enable **Google Drive API**.
3. **APIs & Services → OAuth consent screen** → External; add yourself as a
   test user.
4. **APIs & Services → Credentials → Create credentials → OAuth client ID**
   → application type **Desktop app** → download the JSON.
5. Save the file anywhere; you'll point the app at it on first run.

## Run

```
./gdrive-downloader
```

1. Paste the path to `credentials.json` and the destination folder.
2. Click **Sign in** — a browser window opens, complete the consent flow.
   The token is cached at `~/.config/gdrive-downloader/token.json` (mode 0600)
   and reused on subsequent runs.
3. Click **Scan Drive** to list every non-trashed file in your My Drive.
4. Click **Start download**. Stop at any time; restart to resume.

## Limits

- My Drive only (no shared drives by default).
- Native types other than Docs/Sheets/Slides/Drawings/Apps Script are skipped
  with a log line (Forms, Sites, Jamboards, Fusion Tables, etc.).
- Shortcut entries are skipped — the underlying file is downloaded directly
  via its real entry in the listing.
