# Proxy checker

Currently supports only SOCKS5 proxies

## Running

```
GET_PROXIES_URL="http://localhost:8000/socks5.txt" ADDR=":8080" go run main.go
```

## API

### GET /json

Params:

- code: get only those proxies from country with such code

Example response:

```json
[
    {"ip":"218.65.221.24","port":7302,"countryCode":"CN"},
    {"ip":"123.157.79.246","port":7302,"countryCode":"CN"}
]
```

### GET /proxies.txt

Params:

- code: get only those proxies from country with such code

Example response:

```
129.146.216.187:20000
132.148.159.163:28556
120.224.180.37:7302
39.152.112.205:7302
119.28.26.243:35891
```

