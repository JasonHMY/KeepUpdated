#!/bin/bash
set -euo pipefail
# Keep the sealed App's version/schema checks and native approval bridge together.
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
exec /usr/bin/env -u KEEPUPDATED_BIN /bin/bash "$script_dir/../../../KeepUpdatedAgent.app/Contents/Resources/skills/keepupdated/scripts/run.sh" "$@"
