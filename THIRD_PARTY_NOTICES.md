# Third-party notices

Upstream Pilot is maintained by Tendo33. A distinct name and rewritten product documentation do not make inherited code original.

## Source foundation

This project contains substantial code derived from [S2AM-GO](https://github.com/langrenjh-alt/S2AM-GO), copyright (c) 2026 langrenjh-alt, under the MIT License. The imported upstream revision is [`78d4aa6198f62d57e6231fda2b71a5b2befae72d`](https://github.com/langrenjh-alt/S2AM-GO/tree/78d4aa6198f62d57e6231fda2b71a5b2befae72d).

The original MIT notice is retained in `LICENSE`, together with the notice for this project's modifications. Derived areas include authentication, account/site administration, balance collection, formula calculation, audit infrastructure and interface components. The scheduling-quality extensions are independently implemented. No Guardian source was imported.

## Dependencies

The program also depends on third-party Go and JavaScript modules. These include Chi, pgx, Google UUID, Go supplementary libraries, React, React Router, Radix UI, Lucide and Geist fonts. The authoritative versions are `go.mod`, `go.sum`, `web/package.json` and `web/package-lock.json`.

`THIRD_PARTY_LICENSES.txt` contains license texts collected from installed locked dependencies. Regenerate it after dependency changes with `node scripts/third-party-licenses.mjs`. License notices are bundled with distributed binaries. This file documents attribution, not a claim that all third-party implementations were rewritten.

Some published npm packages omit a license file. For those Radix packages the official primitives repository license is retained in `licenses/radix-primitives.txt`; `react-remove-scroll-bar` uses its official repository license in `licenses/react-remove-scroll-bar.txt`. These are legal notices, not product guides or original-content claims.
