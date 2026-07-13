# PanSou deployment helpers

These scripts update the current Yanhuo deployment from a Windows PowerShell
shell. Defaults match the current server:

- SSH: `root@103.236.97.248 -p 22348`
- Key: `%USERPROFILE%\.ssh\yanhuo`
- Public URL: `http://103.236.97.248:22350/pansou/`
- Remote root: `/opt/pansou-web`

## One-line updates

Backend only:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy\update-backend.ps1
```

Frontend only:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy\update-frontend.ps1
```

Backend and frontend:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy\update-all.ps1
```

## Notes

- `update-backend.ps1` cross-compiles this Go repo for Linux and restarts the
  `pansou-api` Docker container. It also installs an idempotent daily
  PostgreSQL backup job when the host provides `crontab` or `/etc/cron.d`.
  The script reads the `pansou-network` CIDR and passes it as
  `TRUSTED_PROXIES`, so forwarded client IP headers are accepted only from the
  host-side Docker proxy.
- `update-frontend.ps1` expects the sibling frontend repo at `..\pansou-web`,
  builds it with `--base=/pansou/`, uploads `dist`, and reloads Caddy.
- Pass `-EnabledPlugins "a,b,c"` to backend or all scripts to change the plugin
  list during deployment.
- SSH continues to use port `22348`; public HTTP checks use the separate
  `22350 -> server:80` mapping. The provider-side mapping must exist before a
  deployment is run without `-SkipPublicCheck`.
