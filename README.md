# nodemap

An aggregate map representing the reachable node population of Cosmos SDK / CometBFT chains: where nodes are, who hosts
them, which client versions run, how healthy the mesh is, and which public RPC endpoints exist. Built so that it cannot
be turned into a de-anonymizer, and so that its operator never holds a dataset worth stealing.

## What it publishes

Only these fields ever reach the disk. The persisted types have no place for anything else, and the policy is
executable: allowlists pin the exact field set and JSON keys of every persisted type, denylists ban identity, peer, and
topology fields at any depth, individual records are scalars only by structural rule, and aggregate tables carry no key
that would let them be joined back toward a node. Any violation fails the build. The trust root of that guarantee is the
registry of persisted types in `internal/model`: the tests walk exactly those types, and the output package refuses to
encode anything that is not one of them, so a new type cannot reach disk without joining the registry the tests cover.

| Field                                                        | Level                                                                                                                   | Notes                                                                                                          |
|--------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------------------|
| Node counts (answering RPC / observed only as a peer)        | aggregate                                                                                                               | all tiers                                                                                                      |
| Country                                                      | aggregate; per endpoint for self-advertised RPC nodes                                                                   | ISO 3166-1 alpha-2, approximate (IP geolocation)                                                               |
| ASN / hosting organization                                   | aggregate top-N, at least k=5 nodes per row; per endpoint for self-advertised RPC nodes                                 | per row also the share of reported connections landing there, above the population floor                       |
| Client (CometBFT) and application version adoption           | aggregate share only, each above its own population floor; a version run by fewer than k=5 nodes is folded into "other" | never joinable with country, ASN, or endpoint; the application version is known for nodes that answer RPC only |
| Largest connected component, top-N peer-connection share     | two scalars, above a population floor                                                                                   | computed in memory; no edge is stored                                                                          |
| Public RPC endpoint, archive/pruned, tx-indexer, catching-up | per endpoint, self-advertised RPC nodes that do not report voting power                                                 | no version, no moniker on any endpoint                                                                         |

Never published, never persisted: any link between an IP and a validator identity, any peer edge or graph, any node id,
any moniker, per-node software version, per-node IP history, city, or any record of a node that did not answer its own
public RPC. A node that answers RPC and reports voting power, a validator with an open RPC, is counted in every
aggregate and never listed individually.

## How it works

1. Seed from a list of public RPC URLs for the chain, supplied by the instance running the crawl, in the chain-registry
   `chain.json` shape.
2. For each address: `GET /status`, then `GET /net_info`, then `GET /abci_info` for the application version. Three
   requests per address that answers, one for one that does not, each address once, no retries, no other ports,
   redirects refused, bodies capped at 4 MiB. Addresses that do not answer their own RPC are never recorded.
3. Reduce in process, before anything is written: peers become addresses to probe on their own merits and are otherwise
   dropped; the validator identity is never decoded, only whether the node reports voting power, and that only to keep
   it out of the directory; connection direction and statistics are never decoded; the opt-out list is checked here.
4. Enrich IPs with country and ASN from local MaxMind-format (`.mmdb`) databases; the crawler is not tied to any
   provider.
5. Aggregate with k-anonymity and population floors, then write `current.json` (replaced every run) and append one line
   of counters to `history.jsonl`.

Every reduction step is inline in the crawler and not configurable. There is no reducer to swap out and no raw form kept
anywhere.

## Running it

This repository is the software only. A running map is an instance: its own repository holds the seed files, the opt-out
list, the geolocation databases, the delisting key and contact, and the workflow that runs the crawl and publishes the
page. The software names no chain and carries no data.

```
go build -o nodemap .
./nodemap -seeds cosmoshub.json -chain cosmoshub-4 \
  -geo-country dbip-country-lite.mmdb -geo-asn dbip-asn-lite.mmdb -out out
```

| Flag                       | Default  | Meaning                                                                                            |
|----------------------------|----------|----------------------------------------------------------------------------------------------------|
| `-seeds`                   | required | seed file, chain.json shape (`chain_id`, `apis.rpc[].address`); its `chain_id` must match `-chain` |
| `-chain`                   | required | chain-id; nodes and peers on any other network are ignored                                         |
| `-geo-country`, `-geo-asn` | unset    | DB-IP Lite or GeoLite2 `.mmdb`; unset means unknown                                                |
| `-suppress`                | unset    | opt-out list, one salted hash per line                                                             |
| `-out`                     | `out`    | output directory                                                                                   |
| `-workers`                 | 16       | concurrent dials; use 4 on small machines                                                          |
| `-timeout`                 | 5s       | per request                                                                                        |
| `-max-runtime`             | 20m      | a run that does not finish in time writes nothing                                                  |
| `-progress`                | 30s      | counters to stderr this often; 0 for silence                                                       |
| `-down-window`             | 24h      | how long a vanished public endpoint stays listed as down                                           |

Exit codes: 0 completed; 1 nothing publishable (no seed answered, or stopped early) and nothing written; 2 error. Logs
carry counters only, never an address or id.

The salt for the opt-out list comes from the `NODEMAP_SALT` environment variable.
`./nodemap -hash` reads a node id from stdin and prints its salted hash.

## Output

Three files, all stamped with `schema_version` (currently 1) so a consumer that pins a crawler release knows what it
reads; the number changes when a field changes meaning or a file changes shape.

- `current.json`: the full snapshot. Country counts, ASN table, version adoption, mesh scalars, endpoint directory.
  Replaced every run.
- `history.jsonl`: one line per run with timestamp and aggregate counters only. Appended.
- `directory-state.json`: when each public endpoint last answered, pruned after `-down-window`. Endpoints and times
  only; it lets a map show a recently vanished endpoint as down.

The field-by-field reference is `internal/model/model.go`; every published key is also listed in its test.

Endpoints are published as scheme, host, port, and path only. An endpoint with a query string, credentials, or any path
segment of 20 characters or more (the shape of an access token, whatever its alphabet) is left out of the directory; the
node still counts in every aggregate. Client version strings that are not a CometBFT release (0.34 or later), and
application version strings that are not a plain `major.minor.patch`, are reported under "other"; so is any version run
by fewer than k nodes.

A crawl of Cosmos Hub takes about six minutes at 4 workers and 25 MB of memory.

## Known limitations

- Geography is IP geolocation: approximate, and for a node behind a load balancer or CDN it is the front door's location
  unless the node advertises its own listen address, in which case that address is used.
- Most of the observed population (over 90% on Cosmos Hub) does not answer RPC and is known only through the peer lists
  of the nodes that do.
- `HTTP_PROXY` and `HTTPS_PROXY` are honored, as on any CI runner. They change the path, never which addresses are
  dialed. A seed is placed at the address its connection actually reached; through a proxy that address is unknown, so
  the seed's hostname is resolved instead.

## Opting out

The crawler enforces opt-outs; it does not decide them. Deciding that a node may be delisted, whether by proof of
control of its `node_key`, by a DNS record, or by any other process, belongs to the instance running the map, and can
change without touching the crawler.

What the crawler provides is the list. `-suppress FILE` names a text file with one entry per line, blank lines and
`#` comments allowed. An entry is the SHA-256, in lower-case hex, of the salt from `NODEMAP_SALT`, a zero byte, and the
node id in lower-case hex with surrounding whitespace removed. `nodemap -hash` reads a node id on stdin and prints
exactly that entry, so the id never sits on a command line. The salt keeps the published list from revealing who opted
out, since node ids are enumerable by crawling; a non-empty list with no salt is refused.

The check happens at reduction: a node whose id hashes to a listed entry gets no individual record. It still counts in
every aggregate, so opt-out removes the record, not the statistic. It hides the node from the instance's map, not from
anyone running their own crawler. The record disappears with the next crawl after the entry is added.

## Geolocation data and attribution

The crawler works with any MaxMind-format database and ships none. The database you point it at decides what you owe:
the free DB-IP Lite databases are CC BY 4.0 and require attribution to
[DB-IP](https://db-ip.com) wherever results derived from them are shown or redistributed, which means the map page and
the published data files, not this tool. The public instance of this map uses DB-IP Lite and carries that attribution on
the page.