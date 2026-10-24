#!/bin/sh
basedir=$(dirname "$(echo "$0" | sed -e 's,\\,/,g')")

case `uname` in
    *CYGWIN*|*MINGW*|*MSYS*)
        if command -v cygpath > /dev/null 2>&1; then
            basedir=`cygpath -w "$basedir"`
        fi
    ;;
esac

if [ -z "$NODE_PATH" ]; then
  export NODE_PATH="NODE_PATH_VALUE"
else
  export NODE_PATH="NODE_PATH_VALUE:$NODE_PATH"
fi
if [ -x "$basedir/node" ]; then
  exec "$basedir/node"  "$basedir/TARGET" "$@"
else
  exec node  "$basedir/TARGET" "$@"
fi
