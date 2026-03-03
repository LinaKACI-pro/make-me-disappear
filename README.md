# make-me-disappear

Automated GDPR/RGPD personal data deletion bot. Sends deletion requests to data brokers via email, monitors replies via IMAP, and auto-classifies responses (confirmed, no data, needs review).

## Setup

```bash
# 1. Copy and edit config
cp config.example.yaml config.yaml

# 2. Seed the broker database
./bot db-seed

# 3. Create initial requests
./bot campaign-init

# 4. Run the scheduler daemon
./bot run
```

## Commands

| Command | Description |
|---------|-------------|
| `db-seed` | Seed database from `brokers.yaml` |
| `campaign-init` | Create pending requests for all active brokers |
| `run` | Start the scheduler daemon (sends emails, checks inbox, retries) |
| `send --broker ID` | Send a request to a specific broker |
| `imap-check` | Check inbox once and process replies/bounces |
| `inbox-review` | Interactive review of unclassified replies |
| `status` | Show request status summary |
| `manual-list` | List brokers requiring manual action |
| `dashboard` | Generate and open HTML dashboard |
| `purge-bounced` | Remove brokers with bounced emails from DB and YAML |

## Scraper

`cmd/scrape` fetches data broker domains from the YourDigitalRights API and generates a `brokers_scraped.yaml` file to review before seeding.

```bash
go run ./cmd/scrape
```

## Development

```bash
make check          # vet + lint + staticcheck + test + goimports
make build          # build bot and scrape binaries
make install-hooks  # install git pre-commit hook
```

## How it works

1. **Send** -- emails GDPR deletion requests to brokers (FR template for EU, EN otherwise)
2. **Monitor** -- checks IMAP inbox for replies, decodes MIME, classifies via regex patterns
3. **Classify** -- `no_data`, `confirmed`, or `needs_review` based on pattern matching
4. **Retry** -- re-sends after 35 days if no response, marks `manual_required` after 3 attempts
5. **Notify** -- daily digest email if there are items to review
