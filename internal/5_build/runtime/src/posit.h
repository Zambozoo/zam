#pragma once

#include "common.h"

// A pst is a 64-bit posit with two exponent bits, held as its bits. Its order
// is the order of those bits read as a signed integer, and it equals another
// exactly when the bits do, NaR included.
typedef uint64_t ZamPosit;

// ZAMPOSIT_NAR is the one posit that is not a real number. Every operation
// given it answers it, and so does a division by zero.
#define ZAMPOSIT_NAR ((ZamPosit)1 << 63)
#define ZAMPOSIT_ONE ((ZamPosit)1 << 62)

// Every operation rounds to the nearest posit, ties to even. A nonzero result
// never rounds to zero, nor a real one to NaR.
ZamPosit zam_posit_add(ZamPosit left, ZamPosit right);
ZamPosit zam_posit_sub(ZamPosit left, ZamPosit right);
ZamPosit zam_posit_mul(ZamPosit left, ZamPosit right);
ZamPosit zam_posit_div(ZamPosit left, ZamPosit right);

ZamPosit zam_posit_from_int(int64_t value);
ZamPosit zam_posit_from_uint(uint64_t value);
// zam_posit_from_fraction answers the nearest posit to value / 2^64.
ZamPosit zam_posit_from_fraction(uint64_t value);
// zam_posit_from_double answers NaR for a NaN or an infinity.
ZamPosit zam_posit_from_double(double value);

// zam_posit_to_int and zam_posit_to_uint truncate toward zero and saturate.
// NaR is INT64_MIN and 0 respectively, and a negative posit is 0 as a uint.
int64_t zam_posit_to_int(ZamPosit value);
uint64_t zam_posit_to_uint(ZamPosit value);
// zam_posit_to_double answers NaN for NaR.
double zam_posit_to_double(ZamPosit value);

// The elementary functions. Sqrt, Fma, Mod, Remainder, Floor, Ceil, Round
// and Trunc are correctly rounded; the rest are computed with 128 bits and
// rounded once, which is wrong only within about 2^-110 of a tie. Each
// answers NaR for NaR and wherever its real result does not exist.
ZamPosit zam_posit_sqrt(ZamPosit x);
ZamPosit zam_posit_cbrt(ZamPosit x);
ZamPosit zam_posit_exp(ZamPosit x);
ZamPosit zam_posit_exp2(ZamPosit x);
ZamPosit zam_posit_expm1(ZamPosit x);
ZamPosit zam_posit_log(ZamPosit x);
ZamPosit zam_posit_log2(ZamPosit x);
ZamPosit zam_posit_log10(ZamPosit x);
ZamPosit zam_posit_log1p(ZamPosit x);
ZamPosit zam_posit_floor(ZamPosit x);
ZamPosit zam_posit_ceil(ZamPosit x);
// zam_posit_round rounds halves away from zero.
ZamPosit zam_posit_round(ZamPosit x);
ZamPosit zam_posit_trunc(ZamPosit x);
ZamPosit zam_posit_sin(ZamPosit x);
ZamPosit zam_posit_cos(ZamPosit x);
ZamPosit zam_posit_tan(ZamPosit x);
ZamPosit zam_posit_asin(ZamPosit x);
ZamPosit zam_posit_acos(ZamPosit x);
ZamPosit zam_posit_atan(ZamPosit x);
ZamPosit zam_posit_sinh(ZamPosit x);
ZamPosit zam_posit_cosh(ZamPosit x);
ZamPosit zam_posit_tanh(ZamPosit x);
ZamPosit zam_posit_asinh(ZamPosit x);
ZamPosit zam_posit_acosh(ZamPosit x);
ZamPosit zam_posit_atanh(ZamPosit x);
ZamPosit zam_posit_pow(ZamPosit x, ZamPosit y);
// zam_posit_mod is x - n y for n x / y truncated, with the sign of x.
ZamPosit zam_posit_mod(ZamPosit x, ZamPosit y);
// zam_posit_remainder is x - n y for n x / y rounded to even.
ZamPosit zam_posit_remainder(ZamPosit x, ZamPosit y);
ZamPosit zam_posit_hypot(ZamPosit x, ZamPosit y);
ZamPosit zam_posit_atan2(ZamPosit y, ZamPosit x);
// zam_posit_fma is x × y + z rounded once.
ZamPosit zam_posit_fma(ZamPosit x, ZamPosit y, ZamPosit z);
