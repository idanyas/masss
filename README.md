# 🌐 Proxy Results

This branch contains automatically generated and validated proxy lists.

## 📁 Files

- **`all.txt`** - All unique scraped proxies (before validation)
- **`result/http.txt`** - Validated HTTP proxies
- **`result/socks4.txt`** - Validated SOCKS4 proxies
- **`result/socks5.txt`** - Validated SOCKS5 proxies
- **`result/all.json`** - All validated proxies with metadata and aliases

## 🔄 Update Frequency

Automatically updated every 10 minutes via GitHub Actions.

## 📥 Quick Download

### Using curl

```bash
# HTTP proxies
curl -sL https://raw.githubusercontent.com/idanyas/masss/results/result/http.txt

# SOCKS5 proxies
curl -sL https://raw.githubusercontent.com/idanyas/masss/results/result/socks5.txt

# SOCKS4 proxies
curl -sL https://raw.githubusercontent.com/idanyas/masss/results/result/socks4.txt

# All scraped proxies (unvalidated)
curl -sL https://raw.githubusercontent.com/idanyas/masss/results/all.txt

# JSON with full metadata
curl -sL https://raw.githubusercontent.com/idanyas/masss/results/result/all.json
```

### Using wget

```bash
wget https://raw.githubusercontent.com/idanyas/masss/results/result/http.txt
```

## ⚙️ Source Code

The scraper source code and documentation are available on the [main branch](../../tree/main).

## 📊 JSON Format

The `all.json` file groups proxies by public IP and protocol, with aliases for duplicates:

```json
[
  {
    "protocol": "http",
    "address": "1.2.3.4:8080",
    "public_ip": "1.2.3.4",
    "aliases": [
      {"address": "1.2.3.4:8081"}
    ]
  }
]
```

---

*This branch contains only the latest results. History is not preserved to keep the repository lightweight.*
