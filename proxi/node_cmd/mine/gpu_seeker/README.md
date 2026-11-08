# GPU nonce seeker (CUDA reference)

The reference GPU seeker for the transaction-hash work of library upgrade 6
(`kb/library_upgrade6_mining.md`): `proxi node mine` keeps building, verifying, signing and
placing every transit, and hands the nonce search to this program over the seeker protocol,
version 2 (`kb/external_nonce_seeker.md`). It needs no key: a version 2 job carries the bytes to
hash, not a key.

It is the NVIDIA reference. An OpenCL backend of the Rust seeker (`../nonce_seeker`), for AMD and
Intel cards as well, is to follow. VRF jobs (version 1, before the upgrade) are not served: keep
the Rust seeker for those.

## What it does

The work is the transit's own transaction ID: BLAKE2b-256 of the ID preimage, the 8-byte nonce
at its end. For each job the host compresses every block of the preimage before the block holding
the nonce, once, and gives the GPU that state and the remaining one or two blocks. A thread then
hashes only those blocks per attempt and keeps the nonces whose work (bytes 24 to 31 of the hash,
read big-endian) ends in at least K zero bits. The host re-checks each one with Python's own
BLAKE2b, ranks it as the miner does (those 8 bytes, then bytes 6 to 23) and posts the best that
beats the job's best known solution.

Measured on one RTX 5090, 600-byte message: 15.4 G attempts/s at about 560 W.

## Requirements

- An NVIDIA GPU and its driver.
- The CUDA toolkit, for `nvcc` (12.8 or later for RTX 50 cards, `sm_120`; older toolkits for
  older cards).
- Python 3.8 or later, standard library only.

## Build

```bash
nvcc -O3 -arch=sm_120 -Xcompiler -fPIC -shared -o libb2s.so blake2b_search.cu
```

Use the architecture of the card: `sm_89` for RTX 40, `sm_86` for RTX 30, `sm_120` for RTX 50.
`libb2s.so` must sit next to `gpu_seeker.py`.

## Check and measure

```bash
python3 gpu_seeker.py selftest     # GPU solutions equal a CPU search, lengths around block edges
python3 gpu_seeker.py bench 10     # attempts per second for 10 seconds
```

The self-test must pass before mining: it compares the GPU's solutions with a plain CPU search
over the same nonces, on message lengths before, across and after block boundaries.

## Run

In the miner's wallet profile (`proxi.yaml`):

```yaml
mine:
    seeker:
        listen: 127.0.0.1:18555
        token: <a secret of your choice>
```

Then the miner, leaving the search to the seeker, and the seeker:

```bash
proxi node mine --workers 0
python3 gpu_seeker.py run http://127.0.0.1:18555 <token>
```

The listener should stay on the loopback. When the GPU is on another machine, reach it through an
SSH tunnel from the miner's machine (`ssh -N -R 18555:127.0.0.1:18555 gpu-box`), so the GPU box
needs neither the key nor an open port.

Launch size can be set with the environment variables `GRID`, `BLOCK` and `PER_THREAD`; the
defaults (16384, 256, 64) were within 1% of the best on an RTX 5090.
