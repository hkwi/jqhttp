# jqhttp
http reverse proxy with jq transformation, both for request and response.
jqhttp is a kind of api-gateway.

Following example shows github api response body json transformation.

```
JQHTTP_RESPONSE=[.[].name] JQHTTP_UPSTREAM=https://api.github.com/orgs/apache/repos jqhttp
```

```
JQHTTP_REQUEST='{"records":[{"value":.}]}' JQHTTP_UPSTREAM=http://rest-proxy:8082/topics/feed jqhttp
```


## Environment variables

Single jq filter proxy mapping can be configured by environment variables.

- JQHTTP_UPSTREAM : proxy upstream server
- JQHTTP_PATH : jqhttp mapping prefix. defaults to empty string "".
- JQHTTP_REQUEST : jq filter to apply to request body
- JQHTTP_RESPONSE : jq filter to apply to response body

## Configuration YAML

More complex configuration can be done by YAML file.
jqhttp reads `config.yml` by default.
You can specify the filename by `-c` CLI argument.

If path ends with slash "/", then suffix in the request will be passed to upstream.

```
---
listen: ":8080"
routes:
  - path: /kafka
    upstream: http://rest-proxy:8082/topics/http_input
    request: |-
      {"records":[{"value":.}]}
  - path: /apache_repos
    upstream: https://api.github.com/orgs/apache/repos
    response: |-
      [.[].name]
```
