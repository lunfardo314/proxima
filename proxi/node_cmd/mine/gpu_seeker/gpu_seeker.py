#!/usr/bin/env python3
# The reference GPU nonce seeker for the mine chain's transaction-hash work (library upgrade 6),
# protocol version 2 of kb/external_nonce_seeker.md, over blake2b_search.cu built as libb2s.so.
#   gpu_seeker.py selftest            GPU solutions equal a CPU search, lengths around block edges
#   gpu_seeker.py bench [seconds]     attempts per second
#   gpu_seeker.py run URL [TOKEN]     seek for the miner at URL (http://127.0.0.1:PORT)
# Every solution is re-checked with hashlib before it is posted.
import ctypes, hashlib, json, os, random, struct, sys, threading, time, urllib.request

LIB = ctypes.CDLL(os.path.join(os.path.dirname(os.path.abspath(__file__)), "libb2s.so"))
LIB.gpu_setup.argtypes = [ctypes.POINTER(ctypes.c_uint64), ctypes.POINTER(ctypes.c_uint64), ctypes.c_int,
                          ctypes.c_uint64, ctypes.c_uint64, ctypes.c_int, ctypes.c_int]
LIB.gpu_search.argtypes = [ctypes.c_uint64, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_uint32,
                           ctypes.POINTER(ctypes.c_uint64), ctypes.c_uint32, ctypes.POINTER(ctypes.c_uint32)]
# launch size, overridable for tuning: blocks, threads per block, attempts per thread
GRID = int(os.environ.get("GRID", "16384"))
BLOCK = int(os.environ.get("BLOCK", "256"))
PER_THREAD = int(os.environ.get("PER_THREAD", "64"))
MAX_OUT = 4096
M64 = (1 << 64) - 1
IV = [0x6a09e667f3bcc908, 0xbb67ae8584caa73b, 0x3c6ef372fe94f82b, 0xa54ff53a5f1d36f1,
      0x510e527fade682d1, 0x9b05688c2b3e6c1f, 0x1f83d9abfb41bd6b, 0x5be0cd19137e2179]
SIGMA = [[0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15], [14, 10, 4, 8, 9, 15, 13, 6, 1, 12, 0, 2, 11, 7, 5, 3],
         [11, 8, 12, 0, 5, 2, 15, 13, 10, 14, 3, 6, 7, 1, 9, 4], [7, 9, 3, 1, 13, 12, 11, 14, 2, 6, 5, 10, 4, 0, 15, 8],
         [9, 0, 5, 7, 2, 4, 10, 15, 14, 1, 11, 12, 6, 8, 3, 13], [2, 12, 6, 10, 0, 11, 8, 3, 4, 13, 7, 5, 15, 14, 1, 9],
         [12, 5, 1, 15, 14, 13, 4, 10, 0, 7, 6, 3, 9, 2, 8, 11], [13, 11, 7, 14, 12, 1, 3, 9, 5, 0, 15, 4, 8, 6, 2, 10],
         [6, 15, 14, 9, 11, 3, 0, 8, 12, 2, 13, 7, 1, 4, 10, 5], [10, 2, 8, 4, 7, 6, 1, 5, 15, 11, 9, 14, 3, 12, 13, 0]]


def rotr(x, n):
    return ((x >> n) | (x << (64 - n))) & M64


def compress(h, block, t, last):
    m = list(struct.unpack("<16Q", block))
    v = h[:] + IV[:]
    v[12] ^= t & M64
    v[13] ^= t >> 64
    if last:
        v[14] ^= M64
    for r in range(12):
        s = SIGMA[r % 10]
        for i, (a, b, c, d) in enumerate([(0, 4, 8, 12), (1, 5, 9, 13), (2, 6, 10, 14), (3, 7, 11, 15),
                                          (0, 5, 10, 15), (1, 6, 11, 12), (2, 7, 8, 13), (3, 4, 9, 14)]):
            x, y = m[s[2 * i]], m[s[2 * i + 1]]
            v[a] = (v[a] + v[b] + x) & M64; v[d] = rotr(v[d] ^ v[a], 32)
            v[c] = (v[c] + v[d]) & M64; v[b] = rotr(v[b] ^ v[c], 24)
            v[a] = (v[a] + v[b] + y) & M64; v[d] = rotr(v[d] ^ v[a], 16)
            v[c] = (v[c] + v[d]) & M64; v[b] = rotr(v[b] ^ v[c], 63)
    return [h[i] ^ v[i] ^ v[i + 8] for i in range(8)]


def setup(message, k):
    """Loads the job into the GPU: the state after the blocks before the nonce, the tail."""
    n = len(message) + 8
    b0 = len(message) // 128           # the block the nonce starts in
    h = IV[:]
    h[0] ^= 0x01010000 ^ 32            # parameter block: digest length 32, no key
    for i in range(b0):
        h = compress(h, message[128 * i:128 * (i + 1)], 128 * (i + 1), False)
    tail = message[128 * b0:] + bytes(8)
    nblocks = 1 if len(tail) <= 128 else 2
    tail = tail + bytes(128 * nblocks - len(tail))
    words = list(struct.unpack("<%dQ" % (16 * nblocks), tail)) + [0] * (32 - 16 * nblocks)
    t0 = 128 * b0 + (n - 128 * b0 if nblocks == 1 else 128)
    t1 = n
    err = LIB.gpu_setup((ctypes.c_uint64 * 8)(*h), (ctypes.c_uint64 * 32)(*words), nblocks, t0, t1, len(message) - 128 * b0, k)
    assert err == 0, "gpu_setup error %d" % err


def search(start):
    """One launch from start: the nonces meeting k, and the attempts made."""
    out = (ctypes.c_uint64 * MAX_OUT)()
    found = ctypes.c_uint32()
    err = LIB.gpu_search(start, PER_THREAD, GRID, BLOCK, out, MAX_OUT, ctypes.byref(found))
    assert err == 0, "gpu_search error %d" % err
    return [out[i] for i in range(min(found.value, MAX_OUT))], GRID * BLOCK * PER_THREAD


def plain(message, nonce):
    """The hash, its work's trailing zero bits and its 26-byte rank, as the miner has them."""
    s = hashlib.blake2b(message + struct.pack(">Q", nonce), digest_size=32).digest()
    w = int.from_bytes(s[24:32], "big")
    tz = 64 if w == 0 else (w & -w).bit_length() - 1
    return s, tz, s[24:32] + s[6:24]


def selftest():
    global PER_THREAD, GRID
    saved = PER_THREAD, GRID
    PER_THREAD, GRID = 1, 64  # 16384 nonces per launch
    for n in [0, 1, 7, 8, 100, 112, 119, 120, 121, 127, 128, 129, 200, 247, 248, 255, 256, 600, 1000]:
        message = bytes(random.getrandbits(8) for _ in range(n))
        k = 6
        setup(message, k)
        start = random.getrandbits(48)
        got, attempts = search(start)
        want = [x for x in range(start, start + attempts) if plain(message, x)[1] >= k]
        assert sorted(got) == want, "length %d: GPU %d solutions, CPU %d" % (n, len(got), len(want))
        print("length %4d: %3d solutions of %d attempts, equal to the CPU search" % (n, len(want), attempts))
    PER_THREAD, GRID = saved
    print("selftest passed")


def bench(seconds):
    setup(bytes(random.getrandbits(8) for _ in range(600)), 64)
    t, attempts, start = time.time(), 0, 0
    while time.time() - t < seconds:
        _, a = search(start)
        attempts += a
        start += a
    dt = time.time() - t
    print("%.2f G attempts/s (%d attempts in %.1f s, 600-byte message)" % (attempts / dt / 1e9, attempts, dt))


class Miner:
    def __init__(self, url, token):
        self.url, self.token = url.rstrip("/"), token

    def call(self, path, body=None, timeout=35):
        req = urllib.request.Request(self.url + path, data=None if body is None else json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json"})
        if self.token:
            req.add_header("Authorization", "Bearer " + self.token)
        try:
            with urllib.request.urlopen(req, timeout=timeout) as r:
                return r.status, json.loads(r.read() or b"{}")
        except urllib.error.HTTPError as e:
            return e.code, {}


def run(url, token):
    miner = Miner(url, token)
    name = "gpu@" + os.uname().nodename
    job, lock = {"id": 0}, threading.Lock()

    def poll():
        while True:
            with lock:
                after = job.get("id", 0)
            code, j = miner.call("/seeker/job?after=%d&wait=20000" % after)
            if code == 200 and j.get("id") and j["id"] != after:
                with lock:
                    job.clear(); job.update(j)
            elif code != 200:
                time.sleep(1)

    threading.Thread(target=poll, daemon=True).start()
    # unreported: attempts since the last report or result, which is what the miner counts
    current, dead, unreported, last_report = 0, 0, 0, time.time()
    while True:
        with lock:
            j = dict(job)
        if j.get("id") == dead:  # the miner closed this job: wait for the next one
            time.sleep(0.05)
            continue
        if not j.get("id") or j.get("version") != 2:
            if j.get("id") and j.get("version") != 2 and j["id"] != current:
                print("job %d is version %s: not transaction-hash work, waiting" % (j["id"], j.get("version")))
                current = j["id"]
            time.sleep(0.2)
            continue
        if j["id"] != current:
            current, message, k = j["id"], bytes.fromhex(j["message"]), j["k"]
            assert j["nonce_offset"] == len(message), "the nonce must end the message"
            beat = bytes.fromhex(j.get("beat") or "")
            setup(message, k)
            start = random.getrandbits(62)
            print("job %d: slot %d, K %d, message %d bytes%s" % (current, j.get("slot", 0), k, len(message), ", contested" if beat else ""))
        nonces, a = search(start)
        start += a; unreported += a
        best = None
        for x in nonces:
            s, tz, key = plain(message, x)
            if tz >= k and (not beat or key < beat) and (best is None or key < best[2]):
                best = (x, s, key)
        if best:
            code, v = miner.call("/seeker/result", {"id": current, "nonce": struct.pack(">Q", best[0]).hex(),
                                                    "beta": best[1].hex(), "attempts": unreported, "seeker": name})
            unreported = 0
            if code == 200:
                beat = bytes.fromhex(v.get("beat") or "") or best[2]
                print("  %s nonce %016x, work %s" % ("ACCEPTED" if v.get("accepted") else "beaten", best[0], best[2][:8].hex()))
            elif code == 409:
                dead, current = current, 0
            else:
                print("  result refused: HTTP %d %s" % (code, v))
        if time.time() - last_report > 1:
            code, _ = miner.call("/seeker/report", {"id": current, "attempts": unreported, "seeker": name}, timeout=5)
            if code == 200:
                unreported = 0
            last_report = time.time()


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "selftest"
    if cmd == "selftest":
        selftest()
    elif cmd == "bench":
        bench(float(sys.argv[2]) if len(sys.argv) > 2 else 10)
    elif cmd == "run":
        run(sys.argv[2], sys.argv[3] if len(sys.argv) > 3 else "")
