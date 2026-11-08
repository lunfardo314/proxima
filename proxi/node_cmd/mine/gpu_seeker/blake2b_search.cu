// A BLAKE2b-256 nonce search for the mine chain's transaction-hash work (library upgrade 6,
// kb/library_upgrade6_mining.md), driven by gpu_seeker.py.
// The host gives the hash state after the message blocks before the nonce, and the
// tail: the rest of the message and room for the 8-byte big-endian nonce, one or two
// blocks. A thread hashes the tail for its nonces and records those whose work, the
// hash's last 8 bytes read big-endian, ends in at least k zero bits.
#include <stdint.h>
#include <cuda_runtime.h>

__constant__ uint64_t c_h[8];
__constant__ uint64_t c_m[32];
__constant__ int c_nblocks;
__constant__ uint64_t c_t0, c_t1;
__constant__ int c_noff;
__constant__ int c_k;

__constant__ uint64_t IV[8] = {
  0x6a09e667f3bcc908ULL, 0xbb67ae8584caa73bULL, 0x3c6ef372fe94f82bULL, 0xa54ff53a5f1d36f1ULL,
  0x510e527fade682d1ULL, 0x9b05688c2b3e6c1fULL, 0x1f83d9abfb41bd6bULL, 0x5be0cd19137e2179ULL};


__device__ __forceinline__ uint64_t rotr(uint64_t x, int n) { return (x >> n) | (x << (64 - n)); }

#define G(a, b, c, d, x, y)              \
  a = a + b + x; d = rotr(d ^ a, 32);    \
  c = c + d;     b = rotr(b ^ c, 24);    \
  a = a + b + y; d = rotr(d ^ a, 16);    \
  c = c + d;     b = rotr(b ^ c, 63);

// The message words are parameters, so the generated rounds index them by name and
// everything stays in registers.
#define M_PARAMS uint64_t M0, uint64_t M1, uint64_t M2, uint64_t M3, uint64_t M4, uint64_t M5, uint64_t M6, uint64_t M7, \
  uint64_t M8, uint64_t M9, uint64_t M10, uint64_t M11, uint64_t M12, uint64_t M13, uint64_t M14, uint64_t M15
__device__ __forceinline__ void compress(uint64_t h[8], M_PARAMS, uint64_t t, bool last) {
  uint64_t v0 = h[0], v1 = h[1], v2 = h[2], v3 = h[3], v4 = h[4], v5 = h[5], v6 = h[6], v7 = h[7];
  uint64_t v8 = IV[0], v9 = IV[1], v10 = IV[2], v11 = IV[3], v12 = IV[4] ^ t, v13 = IV[5], v14 = IV[6], v15 = IV[7];
  if (last) v14 = ~v14;
  G(v0, v4, v8, v12, M0, M1);
  G(v1, v5, v9, v13, M2, M3);
  G(v2, v6, v10, v14, M4, M5);
  G(v3, v7, v11, v15, M6, M7);
  G(v0, v5, v10, v15, M8, M9);
  G(v1, v6, v11, v12, M10, M11);
  G(v2, v7, v8, v13, M12, M13);
  G(v3, v4, v9, v14, M14, M15);
  G(v0, v4, v8, v12, M14, M10);
  G(v1, v5, v9, v13, M4, M8);
  G(v2, v6, v10, v14, M9, M15);
  G(v3, v7, v11, v15, M13, M6);
  G(v0, v5, v10, v15, M1, M12);
  G(v1, v6, v11, v12, M0, M2);
  G(v2, v7, v8, v13, M11, M7);
  G(v3, v4, v9, v14, M5, M3);
  G(v0, v4, v8, v12, M11, M8);
  G(v1, v5, v9, v13, M12, M0);
  G(v2, v6, v10, v14, M5, M2);
  G(v3, v7, v11, v15, M15, M13);
  G(v0, v5, v10, v15, M10, M14);
  G(v1, v6, v11, v12, M3, M6);
  G(v2, v7, v8, v13, M7, M1);
  G(v3, v4, v9, v14, M9, M4);
  G(v0, v4, v8, v12, M7, M9);
  G(v1, v5, v9, v13, M3, M1);
  G(v2, v6, v10, v14, M13, M12);
  G(v3, v7, v11, v15, M11, M14);
  G(v0, v5, v10, v15, M2, M6);
  G(v1, v6, v11, v12, M5, M10);
  G(v2, v7, v8, v13, M4, M0);
  G(v3, v4, v9, v14, M15, M8);
  G(v0, v4, v8, v12, M9, M0);
  G(v1, v5, v9, v13, M5, M7);
  G(v2, v6, v10, v14, M2, M4);
  G(v3, v7, v11, v15, M10, M15);
  G(v0, v5, v10, v15, M14, M1);
  G(v1, v6, v11, v12, M11, M12);
  G(v2, v7, v8, v13, M6, M8);
  G(v3, v4, v9, v14, M3, M13);
  G(v0, v4, v8, v12, M2, M12);
  G(v1, v5, v9, v13, M6, M10);
  G(v2, v6, v10, v14, M0, M11);
  G(v3, v7, v11, v15, M8, M3);
  G(v0, v5, v10, v15, M4, M13);
  G(v1, v6, v11, v12, M7, M5);
  G(v2, v7, v8, v13, M15, M14);
  G(v3, v4, v9, v14, M1, M9);
  G(v0, v4, v8, v12, M12, M5);
  G(v1, v5, v9, v13, M1, M15);
  G(v2, v6, v10, v14, M14, M13);
  G(v3, v7, v11, v15, M4, M10);
  G(v0, v5, v10, v15, M0, M7);
  G(v1, v6, v11, v12, M6, M3);
  G(v2, v7, v8, v13, M9, M2);
  G(v3, v4, v9, v14, M8, M11);
  G(v0, v4, v8, v12, M13, M11);
  G(v1, v5, v9, v13, M7, M14);
  G(v2, v6, v10, v14, M12, M1);
  G(v3, v7, v11, v15, M3, M9);
  G(v0, v5, v10, v15, M5, M0);
  G(v1, v6, v11, v12, M15, M4);
  G(v2, v7, v8, v13, M8, M6);
  G(v3, v4, v9, v14, M2, M10);
  G(v0, v4, v8, v12, M6, M15);
  G(v1, v5, v9, v13, M14, M9);
  G(v2, v6, v10, v14, M11, M3);
  G(v3, v7, v11, v15, M0, M8);
  G(v0, v5, v10, v15, M12, M2);
  G(v1, v6, v11, v12, M13, M7);
  G(v2, v7, v8, v13, M1, M4);
  G(v3, v4, v9, v14, M10, M5);
  G(v0, v4, v8, v12, M10, M2);
  G(v1, v5, v9, v13, M8, M4);
  G(v2, v6, v10, v14, M7, M6);
  G(v3, v7, v11, v15, M1, M5);
  G(v0, v5, v10, v15, M15, M11);
  G(v1, v6, v11, v12, M9, M14);
  G(v2, v7, v8, v13, M3, M12);
  G(v3, v4, v9, v14, M13, M0);
  G(v0, v4, v8, v12, M0, M1);
  G(v1, v5, v9, v13, M2, M3);
  G(v2, v6, v10, v14, M4, M5);
  G(v3, v7, v11, v15, M6, M7);
  G(v0, v5, v10, v15, M8, M9);
  G(v1, v6, v11, v12, M10, M11);
  G(v2, v7, v8, v13, M12, M13);
  G(v3, v4, v9, v14, M14, M15);
  G(v0, v4, v8, v12, M14, M10);
  G(v1, v5, v9, v13, M4, M8);
  G(v2, v6, v10, v14, M9, M15);
  G(v3, v7, v11, v15, M13, M6);
  G(v0, v5, v10, v15, M1, M12);
  G(v1, v6, v11, v12, M0, M2);
  G(v2, v7, v8, v13, M11, M7);
  G(v3, v4, v9, v14, M5, M3);
  h[0] ^= v0 ^ v8; h[1] ^= v1 ^ v9; h[2] ^= v2 ^ v10; h[3] ^= v3 ^ v11;
  h[4] ^= v4 ^ v12; h[5] ^= v5 ^ v13; h[6] ^= v6 ^ v14; h[7] ^= v7 ^ v15;
}

#define W(j) (c_m[j] | (j == w0 ? lo : 0) | (j == w0 + 1 ? hi : 0))

__global__ void __launch_bounds__(256) search(uint64_t start, uint32_t perThread, uint64_t *out, uint32_t *count, uint32_t maxOut) {
  const uint64_t gid = (uint64_t)blockIdx.x * blockDim.x + threadIdx.x;
  const uint64_t stride = (uint64_t)gridDim.x * blockDim.x;
  const int w0 = c_noff >> 3, sh = 8 * (c_noff & 7);
  for (uint32_t i = 0; i < perThread; i++) {
    const uint64_t nonce = start + gid + (uint64_t)i * stride;
    // the big-endian nonce as a little-endian word, split over the one or two words it covers
    const uint64_t nb = ((uint64_t)__byte_perm((uint32_t)nonce, 0, 0x0123) << 32) | __byte_perm((uint32_t)(nonce >> 32), 0, 0x0123);
    const uint64_t lo = nb << sh, hi = sh ? nb >> (64 - sh) : 0;
    uint64_t h[8];
#pragma unroll
    for (int j = 0; j < 8; j++) h[j] = c_h[j];
    if (c_nblocks == 1) {
      compress(h, W(0), W(1), W(2), W(3), W(4), W(5), W(6), W(7), W(8), W(9), W(10), W(11), W(12), W(13), W(14), W(15), c_t0, true);
    } else {
      compress(h, W(0), W(1), W(2), W(3), W(4), W(5), W(6), W(7), W(8), W(9), W(10), W(11), W(12), W(13), W(14), W(15), c_t0, false);
      compress(h, W(16), W(17), W(18), W(19), W(20), W(21), W(22), W(23), W(24), W(25), W(26), W(27), W(28), W(29), W(30), W(31), c_t1, true);
    }
    // the work: hash bytes 24..31 big-endian, that is h[3] byte-swapped
    const uint64_t w = __byte_perm((uint32_t)(h[3] >> 32), 0, 0x0123) | ((uint64_t)__byte_perm((uint32_t)h[3], 0, 0x0123) << 32);
    const int tz = w == 0 ? 64 : __ffsll((long long)w) - 1;
    if (tz >= c_k) {
      const uint32_t idx = atomicAdd(count, 1);
      if (idx < maxOut) out[idx] = nonce;
    }
  }
}

static uint64_t *d_out = nullptr;
static uint32_t *d_count = nullptr;
static uint32_t d_cap = 0;

extern "C" int gpu_setup(const uint64_t *h, const uint64_t *m, int nblocks, uint64_t t0, uint64_t t1, int noff, int k) {
  cudaMemcpyToSymbol(c_h, h, 8 * sizeof(uint64_t));
  cudaMemcpyToSymbol(c_m, m, 32 * sizeof(uint64_t));
  cudaMemcpyToSymbol(c_nblocks, &nblocks, sizeof(int));
  cudaMemcpyToSymbol(c_t0, &t0, sizeof(uint64_t));
  cudaMemcpyToSymbol(c_t1, &t1, sizeof(uint64_t));
  cudaMemcpyToSymbol(c_noff, &noff, sizeof(int));
  cudaMemcpyToSymbol(c_k, &k, sizeof(int));
  return (int)cudaGetLastError();
}

// gpu_search runs one launch from nonce start and returns the number of nonces meeting k,
// the first maxOut of them in out.
extern "C" int gpu_search(uint64_t start, uint32_t perThread, uint32_t grid, uint32_t block, uint64_t *out, uint32_t maxOut, uint32_t *found) {
  if (d_cap < maxOut) {
    if (d_out) cudaFree(d_out);
    if (!d_count) cudaMalloc(&d_count, sizeof(uint32_t));
    cudaMalloc(&d_out, maxOut * sizeof(uint64_t));
    d_cap = maxOut;
  }
  cudaMemset(d_count, 0, sizeof(uint32_t));
  search<<<grid, block>>>(start, perThread, d_out, d_count, maxOut);
  cudaError_t e = cudaDeviceSynchronize();
  if (e != cudaSuccess) return (int)e;
  cudaMemcpy(found, d_count, sizeof(uint32_t), cudaMemcpyDeviceToHost);
  uint32_t n = *found < maxOut ? *found : maxOut;
  if (n) cudaMemcpy(out, d_out, n * sizeof(uint64_t), cudaMemcpyDeviceToHost);
  return (int)cudaGetLastError();
}
