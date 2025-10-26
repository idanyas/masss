# 🌐 Proxy Results

This branch contains automatically generated and validated proxy lists.

## 📁 Files

- **`all.txt`** - All unique scraped proxies (before validation)
- **`result/http.txt`** - Validated HTTP proxies
- **`result/socks4.txt`** - Validated SOCKS4 proxies
- **`result/socks5.txt`** - Validated SOCKS5 proxies
- **`result/all.json`** - All validated proxies with metadata (flat list)

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

The `all.json` file contains all working proxy+protocol combinations as a flat array:

```json
[
  {
    "protocol": "http",
    "address": "1.2.3.4:8080",
    "public_ip": "5.6.7.8",
    "success_count": 5,
    "latencies_ms": [125.5, 142.3, 138.1, 151.2, 144.7],
    "proxy_geo": {
      "ip": "1.2.3.4",
      "city": "New York",
      "country": "US",
      "asn": {"asn": "AS12345", "name": "Example ISP"}
    },
    "public_geo": {
      "ip": "5.6.7.8",
      "city": "London",
      "country": "GB"
    }
  },
  {
    "protocol": "socks5",
    "address": "1.2.3.4:8080",
    "public_ip": "5.6.7.8",
    "success_count": 4,
    "latencies_ms": [156.2, 149.8, 162.1, 158.4]
  }
]
```

**Note:** If a proxy works with multiple protocols, it appears as separate entries for each protocol.

---

*This branch contains only the latest results. History is not preserved to keep the repository lightweight.*
