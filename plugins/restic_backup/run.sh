#!/usr/bin/env bash
# restic_backup — ductile plugin (protocol v2), bash 3.2 compatible.
# stdin : JSON request {command, job_id, config{env_file, paths[], excludes[], host, tag}}
# stdout: JSON envelope {status, result?, error?, retry?, events[], logs[]}
# Commands: poll (run restic backup), health (repo reachability + creds).
#
# Secrets are sourced from config.env_file (RESTIC_REPOSITORY[, _FALLBACK], RESTIC_PASSWORD),
# never read from job baggage. Credentials are stripped from every emitted string.
# Repo selection uses a bounded curl probe (NOT restic's slow REST retry) so an
# unreachable primary (wire down) fails over to the fallback (Wi-Fi) in seconds.
set -o pipefail

request="$(cat)"
cfg="$(printf '%s' "$request" | jq -c '.config // {}' 2>/dev/null)"
[ -z "$cfg" ] && cfg='{}'

command_val="$(printf '%s' "$request" | jq -r '.command // "poll"' 2>/dev/null)"
[ -z "$command_val" ] && command_val="poll"

cfgr() { printf '%s' "$cfg" | jq -r "$1" 2>/dev/null; }
env_file="$(cfgr '.env_file // ""')"
host="$(cfgr '.host // ""')"
tag="$(cfgr '.tag // "scheduled"')"

emit_ok()  { jq -n --arg r "$1" '{status:"ok",result:$r,events:[],logs:[{level:"info",message:$r}]}'; }
emit_err() { jq -n --arg e "$1" --argjson retry "${2:-false}" '{status:"error",error:$e,retry:$retry,logs:[{level:"error",message:$e}]}'; }
sanitize() { printf '%s' "$1" | sed -E 's#//[^@/]*@#//#'; }   # drop user:pass@ from URLs

# --- resolve restic (gateway spawn PATH may exclude homebrew) ---
RESTIC="$(command -v restic 2>/dev/null || true)"
for c in /opt/homebrew/bin/restic /usr/local/bin/restic /usr/bin/restic; do
  [ -n "$RESTIC" ] && break
  [ -x "$c" ] && RESTIC="$c"
done
[ -n "$RESTIC" ] || { emit_err "restic not found (PATH or known locations)" false; exit 0; }

# --- secrets ---
if [ -n "$env_file" ] && [ -f "$env_file" ]; then set -a; . "$env_file"; set +a; fi
[ -n "${RESTIC_PASSWORD:-}" ] || { emit_err "RESTIC_PASSWORD not set (env_file=${env_file})" false; exit 0; }

# --- choose a reachable repo via bounded curl probe: primary, then fallback ---
probe() {  # $1 = repo URL; rest:URL probed over HTTP /config with a hard 5s cap
  case "$1" in
    rest:*) curl -fsS -m 5 -o /dev/null "${1#rest:}config" 2>/dev/null ;;
    *)      return 0 ;;   # non-rest (local path) — assume usable
  esac
}
repo=""
for cand in "${RESTIC_REPOSITORY:-}" "${RESTIC_REPOSITORY_FALLBACK:-}"; do
  [ -z "$cand" ] && continue
  if probe "$cand"; then repo="$cand"; break; fi
done
[ -n "$repo" ] || { emit_err "no reachable restic repo (primary or fallback)" true; exit 0; }
export RESTIC_REPOSITORY="$repo"
repo_safe="$(sanitize "$repo")"

case "$command_val" in
  health)
    if "$RESTIC" cat config >/dev/null 2>&1; then
      emit_ok "repo reachable + creds ok: ${repo_safe}"; exit 0
    fi
    emit_err "repo ${repo_safe} reachable but restic cat config failed (bad password?)" false; exit 0 ;;
  poll|handle)
    args=(backup); n=0
    while IFS= read -r p; do [ -n "$p" ] && { args+=("$p"); n=$((n+1)); }; done \
      < <(printf '%s' "$cfg" | jq -r '.paths[]?' 2>/dev/null)
    [ "$n" -gt 0 ] || { emit_err "no source paths configured" false; exit 0; }
    args+=(--tag "$tag")
    [ -n "$host" ] && args+=(--host "$host")
    while IFS= read -r e; do [ -n "$e" ] && args+=(--exclude "$e"); done \
      < <(printf '%s' "$cfg" | jq -r '.excludes[]?' 2>/dev/null)
    out="$("$RESTIC" "${args[@]}" 2>&1)"; rc=$?
    if [ "$rc" -eq 0 ]; then
      summary="$(printf '%s' "$out" | grep -E 'Added to the repository|processed [0-9]|snapshot [0-9a-f]+ saved' | tr '\n' ' ' | sed 's/  */ /g')"
      emit_ok "backup ok -> ${repo_safe} : ${summary:-completed}"; exit 0
    else
      tail_out="$(printf '%s' "$out" | tail -4 | tr '\n' ' ' | sed -E 's#//[^@/]*@#//#')"
      emit_err "restic backup failed (rc=${rc}): ${tail_out}" true; exit 0
    fi ;;
  *)
    emit_err "unknown command: ${command_val}" false; exit 0 ;;
esac
