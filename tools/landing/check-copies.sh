#!/usr/bin/env bash
# Checks that the landing page's copies of the web app's tokens, fonts and icon have not drifted,
# that it makes no request but the link to the surface, and that nothing but the page's own files
# sits in the folder Firebase publishes. Run from the repo root: tools/landing/check-copies.sh
set -eu
fail=0
say() { echo "$*"; fail=1; }

# The type files and icons are byte-for-byte copies of what the web app ships.
for f in fonts/IBMPlexSans-400.woff2 fonts/IBMPlexSans-600.woff2 icon.svg favicon.ico; do
  cmp -s "web/public/$f" "landing/$f" || say "landing/$f differs from web/public/$f"
done

# Each custom property the sheet copies from theme.css holds the same value there, ignoring case,
# spaces and quote style.
value() { grep -E -m1 -- "^[[:space:]]*$2:" "$1" | sed -E 's/^[^:]+:[[:space:]]*//; s/[[:space:]]*(\/\*.*)?$//; s/;.*$//' | tr -d " '\"" | tr 'A-F' 'a-f'; }
for p in color-ground color-card color-hair color-ink color-ink2 color-accent font-sans \
  text-hero text-title text-body spacing-gap spacing-pad spacing-padx radius-card; do
  want=$(value web/src/theme.css "--$p")
  got=$(value landing/landing.css "--$p")
  [ -n "$want" ] && [ "$want" = "$got" ] || say "--$p is '$got' in landing/landing.css but '$want' in web/src/theme.css"
done

# The only address in the page is the link to the surface; the sheet names none.
extra=$(grep -noE 'https?://[^"'"'"' )]+' landing/index.html landing/landing.css | grep -v 'forge-wingman-surface-[a-z0-9]*-uc\.a\.run\.app' || true)
[ -z "$extra" ] || say "landing makes a reference beyond the surface link: $extra"
grep -q '@import' landing/landing.css && say "landing/landing.css imports another sheet"

# Firebase publishes everything under landing/ except what firebase.json ignores.
stray=$(find landing -type f ! -name index.html ! -name landing.css ! -name icon.svg ! -name favicon.ico \
  ! -name '*.woff2' ! -name README.md)
[ -z "$stray" ] || say "unexpected files would be published: $stray"
grep -q '"README.md"' firebase.json || say "firebase.json does not ignore landing/README.md"

exit "$fail"
