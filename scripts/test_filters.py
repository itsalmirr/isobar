#!/usr/bin/env python3
"""Test selective-entry filter rules. Each rule restricts entries to a
subset of the paired-trade universe; we compute realized PnL.

The mining step (mine_subsets.py) flagged a few candidate hypotheses:

  H1. Exclude KXHIGHNY entirely (it's $-4,118 of the $-4,964 total)
  H2. Only low-temp brackets (KXLOWT*) — small but reliable
  H3. Only extreme price bands (1-9¢ or 91-99¢) — payoff-ratio favourable
  H4. H1 + H2 + H3 combined
  H5. Specific 3-way grid cells from mine_subsets (h13-18 × edge 10-19 × price 1-24)
  H6. Skip KXHIGHNY + only enter at h_remain ≤ 18 + price 1-90

We compute realized PnL for each filter and rank.
"""

import csv

PATH = "reports/backtest-realized-bayes/edges-2026-04-29_2026-05-04.csv"


def rows():
    with open(PATH) as f:
        for r in csv.DictReader(f):
            if not r["our_side"]:
                continue
            yield r


def evaluate(name, predicate):
    n = won = pnl = fees = 0
    for r in rows():
        if not predicate(r):
            continue
        n += 1
        p = int(r["our_pnl_cents"])
        if p > 0:
            won += 1
        pnl += p
        fees += int(r["fee_cents"])
    if n == 0:
        print(f"  {name:<50} (no trades)")
        return
    print(f"  {name:<50} n={n:>6,}  win={100*won/n:>5.1f}%  PnL={pnl:>+9,}¢  ${pnl/100:>+9.2f}  "
          f"avg={pnl/n:>+6.2f}¢  fee-free=${(pnl+fees)/100:>+9.2f}")


def to_int(s):
    try: return int(s)
    except: return 0


def is_high_ny(r): return r["series"] == "KXHIGHNY"
def is_low(r): return r["series"].startswith("KXLOWT")
def is_extreme(r): p = to_int(r["trade_price_cents"]); return p <= 9 or p >= 91
def h_remain(r): return to_int(r["bayes_h_remain"])
def edge(r): return to_int(r["edge_cents"])
def price(r): return to_int(r["trade_price_cents"])

print("Filter rules (each rule restricts the entry universe; realized PnL on remaining):\n")
print("## Baseline")
evaluate("ALL (no filter)", lambda r: True)

print("\n## H1: exclude KXHIGHNY")
evaluate("not KXHIGHNY", lambda r: not is_high_ny(r))

print("\n## H2: low-temp brackets only")
evaluate("KXLOWT* only", lambda r: is_low(r))

print("\n## H3: extreme-price-band only")
evaluate("price ≤ 9¢ or ≥ 91¢", lambda r: is_extreme(r))

print("\n## H4: union of H1+H2+H3 (most permissive 'profitable' filter)")
evaluate("not KXHIGHNY AND (KXLOWT* OR extreme price)",
         lambda r: not is_high_ny(r) and (is_low(r) or is_extreme(r)))

print("\n## H5: specific 3-way grid cells from mine_subsets")
evaluate("h 13-18 AND edge 10-19 AND price 1-24",
         lambda r: 13 <= h_remain(r) <= 18 and 10 <= edge(r) <= 19 and 1 <= price(r) <= 24)
evaluate("h 13-18 AND edge 20-39 AND price 50-74",
         lambda r: 13 <= h_remain(r) <= 18 and 20 <= edge(r) <= 39 and 50 <= price(r) <= 74)
evaluate("h 13-18 AND edge 5-9 AND price 1-24",
         lambda r: 13 <= h_remain(r) <= 18 and 5 <= edge(r) <= 9 and 1 <= price(r) <= 24)

print("\n## H6: union of top 3 cells from H5")
evaluate("h 13-18 AND profitable (price, edge) cells",
         lambda r:
            13 <= h_remain(r) <= 18 and (
              (10 <= edge(r) <= 19 and 1 <= price(r) <= 24)
              or (20 <= edge(r) <= 39 and 50 <= price(r) <= 74)
              or (5 <= edge(r) <= 9  and 1 <= price(r) <= 24)
            ))

print("\n## H7: skip KXHIGHNY + only extreme-price OR low-temp series + edge ≤ 20¢ (avoid huge-edge traps)")
evaluate("not KXHIGHNY AND (KXLOWT* or extreme price) AND edge≤20",
         lambda r:
            not is_high_ny(r)
            and (is_low(r) or is_extreme(r))
            and edge(r) <= 20)

print("\n## H8: H4 but only enter when bayes_obs_weight < 0.30 (kernel doesn't dominate)")
evaluate("H4 + obs_weight < 0.30",
         lambda r:
            not is_high_ny(r)
            and (is_low(r) or is_extreme(r))
            and float(r["bayes_obs_weight"] or 0) < 0.30)

print("\n## H9: deep-OTM only — most-asymmetric payoff bets")
evaluate("price ≤ 5¢ or ≥ 95¢", lambda r: price(r) <= 5 or price(r) >= 95)
evaluate("price ≤ 3¢ or ≥ 97¢", lambda r: price(r) <= 3 or price(r) >= 97)
evaluate("price ≤ 9¢ or ≥ 91¢", lambda r: price(r) <= 9 or price(r) >= 91)

print("\n## H10: union of best single-station picks + extreme prices")
evaluate("series in {KXLOWTMIA, KXLOWTLAX, KXLOWTCHI, KXHIGHLAX} OR extreme price",
         lambda r:
            r["series"] in {"KXLOWTMIA", "KXLOWTLAX", "KXLOWTCHI", "KXHIGHLAX"}
            or is_extreme(r))

print("\n## H11: H10 but extra-selective: edge ≥ 10")
evaluate("H10 AND edge ≥ 10",
         lambda r: (
            r["series"] in {"KXLOWTMIA", "KXLOWTLAX", "KXLOWTCHI", "KXHIGHLAX"}
            or is_extreme(r)
         ) and edge(r) >= 10)
