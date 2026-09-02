# nodemap

An aggregate map representing the reachable node population of Cosmos SDK / CometBFT chains: where nodes are, who hosts
them, which client versions run, how healthy the mesh is, and which public RPC endpoints exist. Built so that it cannot
be turned into a de-anonymizer, and so that its operator never holds a dataset worth stealing.

## What it publishes

Only these fields ever reach the disk. The persisted types have no place for anything else, and a test fails the build
if a field with a forbidden name is added.

| Field                                                        | Level                                                                                   | Notes                                            |
|--------------------------------------------------------------|-----------------------------------------------------------------------------------------|--------------------------------------------------|
| Node counts (answering RPC / observed only as a peer)        | aggregate                                                                               | all tiers                                        |
| Country                                                      | aggregate; per endpoint for self-advertised RPC nodes                                   | ISO 3166-1 alpha-2, approximate (IP geolocation) |
| ASN / hosting organization                                   | aggregate top-N, at least k=5 nodes per row; per endpoint for self-advertised RPC nodes |                                                  |
| Client version adoption                                      | aggregate share only, above a population floor                                          | never joinable with country, ASN, or endpoint    |
| Largest connected component, top-N peer-connection share     | two scalars, above a population floor                                                   | computed in memory; no edge is stored            |
| Public RPC endpoint, archive/pruned, tx-indexer, catching-up | per endpoint, self-advertised RPC nodes that do not report voting power                 | no version, no moniker on any endpoint           |

Never published, never persisted: any link between an IP and a validator identity, any peer edge or graph, any node id,
any moniker, per-node software version, per-node IP history, city, or any record of a node that did not answer its own
public RPC. A node that answers RPC and reports voting power, a validator with an open RPC, is counted in every
aggregate and never listed individually.

## How it works

1. Seed from a checked-in list of public RPC URLs per chain (`seeds/<chain>.json`, in the chain-registry `chain.json`
   shape).
2. For each address: `GET /status`, then `GET /net_info`. Two requests per address per run, each address once, no
   retries, no other ports, redirects refused, bodies capped at 4 MiB. Addresses that do not answer their own RPC are
   never recorded.
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

```
go build -o nodemap .
./nodemap -seeds seeds/cosmoshub.json -chain cosmoshub-4 \
  -geo-country dbip-country-lite.mmdb -geo-asn dbip-asn-lite.mmdb -out out
```

| Flag                       | Default                | Meaning                                                                                            |
|----------------------------|------------------------|----------------------------------------------------------------------------------------------------|
| `-seeds`                   | `seeds/cosmoshub.json` | seed file, chain.json shape (`chain_id`, `apis.rpc[].address`); its `chain_id` must match `-chain` |
| `-chain`                   | `cosmoshub-4`          | chain-id; nodes and peers on any other network are ignored                                         |
| `-geo-country`, `-geo-asn` | unset                  | DB-IP Lite or GeoLite2 `.mmdb`; unset means unknown                                                |
| `-suppress`                | unset                  | opt-out list, one salted hash per line                                                             |
| `-out`                     | `out`                  | output directory                                                                                   |
| `-workers`                 | 16                     | concurrent dials; use 4 on small machines                                                          |
| `-timeout`                 | 5s                     | per request                                                                                        |
| `-max-runtime`             | 20m                    | a run that does not finish in time writes nothing                                                  |
| `-progress`                | 30s                    | counters to stderr this often; 0 for silence                                                       |
| `-down-window`             | 24h                    | how long a vanished public endpoint stays listed as down                                           |

Exit codes: 0 completed; 1 nothing publishable (no seed answered, or stopped early) and nothing written; 2 error. Logs
carry counters only, never an address or id.

The salt for the opt-out list comes from the `NODEMAP_SALT` environment variable.
`./nodemap -hash` reads a node id from stdin and prints its salted hash.

## Output

- `current.json`: the full snapshot. Country counts, ASN table, version adoption, mesh scalars, endpoint directory.
  Replaced every run.
- `history.jsonl`: one line per run with timestamp and aggregate counters only. Appended.
- `directory-state.json`: when each public endpoint last answered, pruned after `-down-window`. Endpoints and times
  only; it lets the map show a recently vanished endpoint as down.

Endpoints are published as scheme, host, port, and path only. An endpoint with a query string, credentials, or any path
segment of 20 characters or more (the shape of an access token, whatever its alphabet) is left out of the directory; the
node still counts in every aggregate. Version strings that are not a CometBFT release (0.34 or later) are reported under
"other".

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

Operators of a self-advertised RPC node can have its individual record removed. Removal requires proof of control of the
node's `node_key` (the P2P identity key, not the consensus key; it cannot sign blocks). The procedure, the signed
message format, and where to send the encrypted proof are documented in this section once the verification helper ships.
Opt-out removes the endpoint record; the node still counts in anonymous aggregates. It hides the node from this map, not
from anyone running their own crawler.

## Geolocation data and attribution

The crawler works with any MaxMind-format database and ships none. The database you point it at
decides what you owe: the free DB-IP Lite databases are CC BY 4.0 and require attribution to
[DB-IP](https://db-ip.com) wherever results derived from them are shown or redistributed, which
means the map page and the published data files, not this tool. The public instance of this
map uses DB-IP Lite and carries that attribution on the page.