#!/bin/sh

if [ ${RUN_MODE} == "server" ] ; then
  exec /app/server \
      --timeout=${SERVER_TIMEOUT} \
      --log-level=${SERVER_LOG_LEVEL} \
      --poll-timeout=${SERVER_POLL_TIMEOUT} \
      --web-url=${SERVER_WEB_URL}

elif [ ${RUN_MODE} == "client" ] ; then
  exec /app/client \
      --proxy-url=${CLIENT_PROXY_URL} \
      --remote-fqdn=${CLIENT_REMOTE_FQDN} \
      --log-level=${CLIENT_LOG_LEVEL} \
      --heartbeat=${CLIENT_HEARTBEAT}
fi
