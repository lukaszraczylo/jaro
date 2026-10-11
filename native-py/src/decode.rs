//! JARO binary decoder building CPython objects directly.
//!
//! Every input-dependent access is bounds-checked and failures return `Fail`,
//! never panic. Strong references are held in `Owned` so every error path
//! releases them exactly once.
use crate::err::{bad, py_fail, Fail, R};
use crate::pyo::{boolean, none, Owned};
use pyo3::ffi;
use std::collections::HashSet;
use std::os::raw::c_char;

const MAGIC: u8 = 0x0F;
const VERSION: u8 = 0x01;
const T_NULL: u8 = 0;
const T_FALSE: u8 = 1;
const T_TRUE: u8 = 2;
const T_INT: u8 = 3;
const T_FLOAT: u8 = 4;
const T_STR: u8 = 5;
const T_REF: u8 = 6;
const T_ARR: u8 = 7;
const T_OBJ: u8 = 8;
const T_TABLE: u8 = 9;
const T_ANCHOR: u8 = 0x0A;
const T_ANCHOR_REF: u8 = 0x0B;
const T_COL: u8 = 0x0C;
const T_SPARSE: u8 = 0x0D;
const T_SHORT_STR: u8 = 0x40;
const T_SMALL_INT: u8 = 0x80;

const K_GENERIC: u8 = 0;
const K_DELTA: u8 = 1;
const K_DECIMAL: u8 = 2;
const K_DICT: u8 = 3;
const K_BOOL: u8 = 4;
const K_OBJECT: u8 = 5;

const MIN_POOLED: usize = 2;
const MAX_COUNT: u64 = 0x7FFF_FFFF;
const MAX_DEPTH: usize = 512;
const COPY_DEPTH_GUARD: usize = 4096;
const NODES_BASE: i64 = 1 << 22;
const NODES_PER_BYTE: i64 = 32;
const MAX_EXPAND: i64 = 1 << 24;
const MAX_DECIMALS: u8 = 6;
const MAX_EXACT: f64 = 9_007_199_254_740_992.0;
const POW10: [f64; 7] = [1.0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6];
const POW10_INT: [i64; 7] = [1, 10, 100, 1_000, 10_000, 100_000, 1_000_000];
const DICT_LINEAR_MAX: usize = 16;

struct PoolEnt {
    obj: Owned,
    off: usize,
    len: usize,
}

struct Key {
    obj: Owned,
    off: usize,
    len: usize,
}

/// Receives the values of one column; takes ownership of every reference passed to it.
trait Sink {
    fn put(&mut self, i: usize, v: *mut ffi::PyObject) -> R<()>;
}

struct ListSink {
    list: *mut ffi::PyObject,
}

impl Sink for ListSink {
    #[inline(always)]
    fn put(&mut self, i: usize, v: *mut ffi::PyObject) -> R<()> {
        // PyList_SetItem steals v, also when it fails.
        if unsafe { ffi::PyList_SetItem(self.list, i as ffi::Py_ssize_t, v) } != 0 {
            return py_fail();
        }
        Ok(())
    }
}

/// Stores value i under `key` in the i-th row dict.
struct DictSink<'a> {
    rows: &'a [*mut ffi::PyObject],
    key: *mut ffi::PyObject,
}

impl Sink for DictSink<'_> {
    #[inline(always)]
    fn put(&mut self, i: usize, v: *mut ffi::PyObject) -> R<()> {
        let v = Owned::new(v).ok_or(Fail::Py)?;
        let Some(&row) = self.rows.get(i) else {
            return bad("internal row index", 0);
        };
        if unsafe { ffi::PyDict_SetItem(row, self.key, v.ptr()) } != 0 {
            return py_fail();
        }
        Ok(())
    }
}

/// Like DictSink but only for rows whose presence bit is set, in row order.
struct SparseSink<'a> {
    rows: &'a [*mut ffi::PyObject],
    bits: &'a [u8],
    cur: usize,
    key: *mut ffi::PyObject,
}

impl Sink for SparseSink<'_> {
    #[inline(always)]
    fn put(&mut self, _i: usize, v: *mut ffi::PyObject) -> R<()> {
        let v = Owned::new(v).ok_or(Fail::Py)?;
        loop {
            let Some(&row) = self.rows.get(self.cur) else {
                return bad("bad presence bitmap", 0);
            };
            let i = self.cur;
            self.cur += 1;
            let set = self
                .bits
                .get(i >> 3)
                .is_some_and(|b| (b >> (i & 7)) & 1 == 1);
            if set {
                if unsafe { ffi::PyDict_SetItem(row, self.key, v.ptr()) } != 0 {
                    return py_fail();
                }
                return Ok(());
            }
        }
    }
}

#[inline(always)]
fn own(p: *mut ffi::PyObject) -> R<Owned> {
    Owned::new(p).ok_or(Fail::Py)
}

#[inline(always)]
fn unzig(u: u64) -> i64 {
    ((u >> 1) as i64) ^ -((u & 1) as i64)
}

pub struct Decoder<'a> {
    s: &'a [u8],
    pos: usize,
    pool: Vec<PoolEnt>,
    anchors: Vec<Option<Owned>>,
    budget: i64,
    nodes: i64,
}

/// Decodes a whole document.
pub fn decode(data: &[u8]) -> R<Owned> {
    if data.len() < 2 || data[0] != MAGIC {
        return bad("not a JARO binary document", 0);
    }
    if data[1] != VERSION {
        return bad(&format!("unsupported binary version {}", data[1]), 1);
    }
    let mut d = Decoder {
        s: data,
        pos: 2,
        pool: Vec::new(),
        anchors: Vec::new(),
        budget: MAX_EXPAND,
        nodes: NODES_BASE + NODES_PER_BYTE * data.len() as i64,
    };
    let v = d.value(0)?;
    if d.pos != d.s.len() {
        return bad("trailing data", d.pos);
    }
    Ok(v)
}

impl Decoder<'_> {
    #[inline(always)]
    fn remaining(&self) -> usize {
        self.s.len().saturating_sub(self.pos)
    }

    #[cold]
    #[inline(never)]
    fn short<T>(&self) -> R<T> {
        bad("truncated input", self.pos)
    }

    #[inline(always)]
    fn uvarint(&mut self) -> R<u64> {
        if let Some(&b) = self.s.get(self.pos) {
            if b < 0x80 {
                self.pos += 1;
                return Ok(b as u64);
            }
        }
        self.uvarint_slow()
    }

    #[inline(never)]
    fn uvarint_slow(&mut self) -> R<u64> {
        let start = self.pos;
        let mut x = 0u64;
        for i in 0..10usize {
            let Some(&b) = self.s.get(start + i) else {
                return self.short();
            };
            if b < 0x80 {
                if i == 9 && b > 1 {
                    return bad("varint overflows 64 bits", start);
                }
                self.pos = start + i + 1;
                return Ok(x | ((b as u64) << (7 * i)));
            }
            x |= ((b & 0x7F) as u64) << (7 * i);
        }
        bad("varint overflows 64 bits", start)
    }

    /// An element count; it cannot exceed the bytes left.
    #[inline(always)]
    fn count(&mut self) -> R<usize> {
        let n = self.uvarint()?;
        if n > MAX_COUNT || n > self.remaining() as u64 {
            return self.short();
        }
        Ok(n as usize)
    }

    /// A row count; bool columns take one bit per value, so the bound is 8x the bytes left.
    fn row_count(&mut self) -> R<usize> {
        let n = self.uvarint()?;
        if n > MAX_COUNT || n > (self.remaining() as u64).saturating_mul(8) {
            return self.short();
        }
        Ok(n as usize)
    }

    #[inline(always)]
    fn charge(&mut self, n: usize) -> R<()> {
        self.nodes -= n as i64;
        if self.nodes < 0 {
            return bad("decoded size too large", self.pos);
        }
        Ok(())
    }

    /// Fails like `charge(n)` would, without spending the budget.
    fn precheck(&self, n: usize) -> R<()> {
        if self.nodes < n as i64 {
            return bad("decoded size too large", self.pos);
        }
        Ok(())
    }

    #[cold]
    #[inline(never)]
    fn utf8_fail<T>(&self, pos: usize) -> R<T> {
        unsafe {
            if ffi::PyErr_ExceptionMatches(ffi::PyExc_UnicodeDecodeError) != 0 {
                ffi::PyErr_Clear();
                return bad("string is not valid UTF-8", pos);
            }
        }
        py_fail()
    }

    #[inline(always)]
    fn str_bytes(&mut self, n: usize) -> R<Key> {
        let pos = self.pos;
        if n > self.remaining() {
            return self.short();
        }
        let p = unsafe {
            ffi::PyUnicode_FromStringAndSize(
                self.s.as_ptr().add(pos) as *const c_char,
                n as ffi::Py_ssize_t,
            )
        };
        let Some(obj) = Owned::new(p) else {
            return self.utf8_fail(pos);
        };
        self.pos = pos + n;
        if n >= MIN_POOLED {
            self.pool.push(PoolEnt {
                obj: Owned::from_borrowed(obj.ptr()),
                off: pos,
                len: n,
            });
        }
        Ok(Key {
            obj,
            off: pos,
            len: n,
        })
    }

    /// Reads a pool reference body (after the tag).
    #[inline(always)]
    fn pool_ref(&mut self) -> R<&PoolEnt> {
        let idx = self.uvarint()?;
        match usize::try_from(idx).ok().and_then(|i| self.pool.get(i)) {
            Some(e) => Ok(e),
            None => bad("bad string reference", self.pos),
        }
    }

    /// Reads a string used as an object key or table column.
    #[inline(always)]
    fn key(&mut self) -> R<Key> {
        let pos = self.pos;
        let Some(&t) = self.s.get(pos) else {
            return self.short();
        };
        self.pos = pos + 1;
        match t {
            T_REF => {
                let e = self.pool_ref()?;
                Ok(Key {
                    obj: Owned::from_borrowed(e.obj.ptr()),
                    off: e.off,
                    len: e.len,
                })
            }
            T_SHORT_STR..=0x5F => self.str_bytes((t & 31) as usize),
            T_STR => {
                let n = self.count()?;
                self.str_bytes(n)
            }
            _ => bad("key must be a string", pos),
        }
    }

    /// Reads nk keys that must be unique.
    fn unique_keys(&mut self, nk: usize, dup: &str) -> R<Vec<Key>> {
        let mut keys: Vec<Key> = Vec::with_capacity(nk);
        let mut seen: HashSet<&[u8]> = HashSet::new();
        let s = self.s;
        for _ in 0..nk {
            let k = self.key()?;
            let bytes = s.get(k.off..k.off + k.len).unwrap_or_default();
            let is_dup = if nk > DICT_LINEAR_MAX {
                !seen.insert(bytes)
            } else {
                keys.iter()
                    .any(|o| s.get(o.off..o.off + o.len).unwrap_or_default() == bytes)
            };
            if is_dup {
                return bad(
                    &format!("{dup} '{}'", String::from_utf8_lossy(bytes)),
                    self.pos,
                );
            }
            keys.push(k);
        }
        Ok(keys)
    }

    fn header(&mut self) -> R<Vec<Key>> {
        let nc = self.count()?;
        if nc == 0 {
            return bad("empty table header", self.pos);
        }
        self.unique_keys(nc, "duplicate column")
    }

    pub fn value(&mut self, depth: usize) -> R<Owned> {
        if depth > MAX_DEPTH {
            return bad("nesting too deep", self.pos);
        }
        let pos = self.pos;
        let Some(&t) = self.s.get(pos) else {
            return self.short();
        };
        self.pos = pos + 1;
        match t {
            T_SMALL_INT..=0xFF => own(unsafe { ffi::PyLong_FromLong((t & 0x7F) as _) }),
            T_SHORT_STR..=0x5F => Ok(self.str_bytes((t & 31) as usize)?.obj),
            T_ARR => self.array(depth),
            T_REF => {
                let e = self.pool_ref()?;
                Ok(Owned::from_borrowed(e.obj.ptr()))
            }
            T_NULL => own(none()),
            T_FALSE => own(boolean(false)),
            T_TRUE => own(boolean(true)),
            T_INT => {
                let u = self.uvarint()?;
                own(unsafe { ffi::PyLong_FromLongLong(unzig(u)) })
            }
            T_FLOAT => self.float(),
            T_STR => {
                let n = self.count()?;
                Ok(self.str_bytes(n)?.obj)
            }
            T_OBJ => self.object(depth),
            T_TABLE => self.table(depth),
            T_SPARSE => self.sparse(depth),
            T_COL => {
                let n = self.row_count()?;
                self.precheck(n + 1)?;
                let list = own(unsafe { ffi::PyList_New(n as ffi::Py_ssize_t) })?;
                let mut sink = ListSink { list: list.ptr() };
                self.column(n, depth + 1, &mut sink)?;
                Ok(list)
            }
            T_ANCHOR => self.anchor(depth),
            T_ANCHOR_REF => self.anchor_ref(),
            _ => bad(&format!("bad tag 0x{t:02x}"), pos),
        }
    }

    #[inline(never)]
    fn float(&mut self) -> R<Owned> {
        let Some(raw) = self.s.get(self.pos..self.pos + 8) else {
            return self.short();
        };
        let mut b = [0u8; 8];
        b.copy_from_slice(raw);
        let f = f64::from_le_bytes(b);
        if !f.is_finite() {
            return bad("float is not finite", self.pos);
        }
        self.pos += 8;
        make_number(f)
    }

    #[inline(never)]
    fn array(&mut self, depth: usize) -> R<Owned> {
        let cnt = self.count()?;
        let list = own(unsafe { ffi::PyList_New(cnt as ffi::Py_ssize_t) })?;
        let mut sink = ListSink { list: list.ptr() };
        for i in 0..cnt {
            let v = self.value(depth + 1)?;
            sink.put(i, v.into_raw())?;
        }
        Ok(list)
    }

    #[inline(never)]
    fn object(&mut self, depth: usize) -> R<Owned> {
        let cnt = self.count()?;
        let d = own(unsafe { ffi::PyDict_New() })?;
        for _ in 0..cnt {
            // A pool reference needs no refcount traffic: the pool keeps the string alive.
            let (kp, koff, klen, _held) = if self.s.get(self.pos) == Some(&T_REF) {
                self.pos += 1;
                let e = self.pool_ref()?;
                (e.obj.ptr(), e.off, e.len, None)
            } else {
                let k = self.key()?;
                (k.obj.ptr(), k.off, k.len, Some(k.obj))
            };
            let v = self.value(depth + 1)?;
            let before = unsafe { ffi::PyDict_Size(d.ptr()) };
            if unsafe { ffi::PyDict_SetItem(d.ptr(), kp, v.ptr()) } != 0 {
                return py_fail();
            }
            if unsafe { ffi::PyDict_Size(d.ptr()) } == before {
                let kb = self.s.get(koff..koff + klen).unwrap_or_default();
                return bad(
                    &format!("duplicate key '{}'", String::from_utf8_lossy(kb)),
                    self.pos,
                );
            }
        }
        Ok(d)
    }

    /// Allocates a list of n empty dicts; the second result borrows them from the list.
    fn row_dicts(&self, n: usize) -> R<(Owned, Vec<*mut ffi::PyObject>)> {
        let list = own(unsafe { ffi::PyList_New(n as ffi::Py_ssize_t) })?;
        let mut rows = Vec::with_capacity(n);
        for i in 0..n {
            let d = own(unsafe { ffi::PyDict_New() })?.into_raw();
            // The list owns d from here on (PyList_SetItem steals it).
            if unsafe { ffi::PyList_SetItem(list.ptr(), i as ffi::Py_ssize_t, d) } != 0 {
                return py_fail();
            }
            rows.push(d);
        }
        Ok((list, rows))
    }

    #[inline(never)]
    fn table(&mut self, depth: usize) -> R<Owned> {
        let keys = self.header()?;
        let nr = self.row_count()?;
        self.precheck(nr + 1)?;
        let (list, rows) = self.row_dicts(nr)?;
        for k in &keys {
            let mut sink = DictSink {
                rows: &rows,
                key: k.obj.ptr(),
            };
            self.column(nr, depth + 1, &mut sink)?;
        }
        Ok(list)
    }

    #[inline(never)]
    fn sparse(&mut self, depth: usize) -> R<Owned> {
        let keys = self.header()?;
        let nr = self.row_count()?;
        self.charge(nr * 2)?;
        let (list, rows) = self.row_dicts(nr)?;
        for k in &keys {
            let Some(&flag) = self.s.get(self.pos) else {
                return self.short();
            };
            self.pos += 1;
            let mut bits: &[u8] = &[];
            let mut n = nr;
            match flag {
                0 => {}
                1 => {
                    let nb = nr.div_ceil(8);
                    let Some(b) = self.s.get(self.pos..self.pos + nb) else {
                        return self.short();
                    };
                    bits = b;
                    self.pos += nb;
                    n = bits.iter().map(|x| x.count_ones() as usize).sum();
                    let pad = nr & 7 != 0 && bits.last().is_some_and(|&l| l >> (nr & 7) != 0);
                    if n > nr || pad {
                        return bad("bad presence bitmap", self.pos);
                    }
                }
                _ => return bad("bad presence flag", self.pos),
            }
            if flag == 0 {
                let mut sink = DictSink {
                    rows: &rows,
                    key: k.obj.ptr(),
                };
                self.column(n, depth + 1, &mut sink)?;
            } else {
                let mut sink = SparseSink {
                    rows: &rows,
                    bits,
                    cur: 0,
                    key: k.obj.ptr(),
                };
                self.column(n, depth + 1, &mut sink)?;
            }
        }
        Ok(list)
    }

    #[inline(never)]
    fn anchor(&mut self, depth: usize) -> R<Owned> {
        let idx = self.anchors.len();
        self.anchors.push(None);
        let v = self.value(depth + 1)?;
        if let Some(slot) = self.anchors.get_mut(idx) {
            *slot = Some(Owned::from_borrowed(v.ptr()));
        }
        Ok(v)
    }

    #[inline(never)]
    fn anchor_ref(&mut self) -> R<Owned> {
        let idx = self.uvarint()?;
        let src = usize::try_from(idx)
            .ok()
            .and_then(|i| self.anchors.get(i))
            .and_then(|a| a.as_ref())
            .map(|a| a.ptr());
        let Some(src) = src else {
            return bad("bad anchor reference", self.pos);
        };
        let mut budget = self.budget;
        let r = deep_copy(src, &mut budget, 0);
        self.budget = budget;
        match r {
            Ok(Some(v)) => Ok(v),
            Ok(None) => bad("reference expansion too large", self.pos),
            Err(f) => Err(f),
        }
    }

    fn column<S: Sink>(&mut self, n: usize, depth: usize, sink: &mut S) -> R<()> {
        if depth > MAX_DEPTH {
            return bad("nesting too deep", self.pos);
        }
        self.charge(n + 1)?;
        let Some(&kind) = self.s.get(self.pos) else {
            return self.short();
        };
        self.pos += 1;
        match kind {
            K_GENERIC => {
                for i in 0..n {
                    let v = self.value(depth + 1)?;
                    sink.put(i, v.into_raw())?;
                }
            }
            K_DELTA => {
                let mut prev: i64 = 0;
                for i in 0..n {
                    let u = self.uvarint()?;
                    prev = prev.wrapping_add(unzig(u));
                    let o = own(unsafe { ffi::PyLong_FromLongLong(prev) })?;
                    sink.put(i, o.into_raw())?;
                }
            }
            K_DECIMAL => self.decimal_column(n, sink)?,
            K_DICT => {
                let nd = self.count()?;
                let mut dict: Vec<Key> = Vec::with_capacity(nd);
                for _ in 0..nd {
                    dict.push(self.key()?);
                }
                for i in 0..n {
                    let idx = self.uvarint()?;
                    let Some(k) = usize::try_from(idx).ok().and_then(|x| dict.get(x)) else {
                        return bad("bad dictionary index", self.pos);
                    };
                    sink.put(i, k.obj.new_ref())?;
                }
            }
            K_BOOL => {
                let nb = n.div_ceil(8);
                let Some(bytes) = self.s.get(self.pos..self.pos + nb) else {
                    return self.short();
                };
                for i in 0..n {
                    let b = bytes.get(i >> 3).copied().unwrap_or(0);
                    sink.put(i, boolean((b >> (i & 7)) & 1 == 1))?;
                }
                self.pos += nb;
            }
            K_OBJECT => {
                let nk = self.count()?;
                if nk == 0 {
                    return bad("empty object column", self.pos);
                }
                let keys = self.unique_keys(nk, "duplicate key")?;
                let mut maps: Vec<Owned> = Vec::with_capacity(n);
                let mut raw: Vec<*mut ffi::PyObject> = Vec::with_capacity(n);
                for _ in 0..n {
                    let d = own(unsafe { ffi::PyDict_New() })?;
                    raw.push(d.ptr());
                    maps.push(d);
                }
                for k in &keys {
                    let mut inner = DictSink {
                        rows: &raw,
                        key: k.obj.ptr(),
                    };
                    self.column(n, depth + 1, &mut inner)?;
                }
                for (i, m) in maps.into_iter().enumerate() {
                    sink.put(i, m.into_raw())?;
                }
            }
            _ => return bad("bad column kind", self.pos - 1),
        }
        Ok(())
    }

    fn decimal_column<S: Sink>(&mut self, n: usize, sink: &mut S) -> R<()> {
        let Some(&k) = self.s.get(self.pos) else {
            return bad("bad decimal scale", self.pos);
        };
        if k > MAX_DECIMALS {
            return bad("bad decimal scale", self.pos);
        }
        self.pos += 1;
        let k = k as usize;
        for i in 0..n {
            let m = unzig(self.uvarint()?);
            let o = if k == 0 {
                own(unsafe { ffi::PyLong_FromLongLong(m) })?
            } else if m.unsigned_abs() <= 1 << 53 {
                make_number(m as f64 / POW10[k])?
            } else {
                big_decimal(m, k)?
            };
            sink.put(i, o.into_raw())?;
        }
        Ok(())
    }
}

/// Applies the data model rule: integral floats below 2^53 are ints.
#[inline(always)]
fn make_number(f: f64) -> R<Owned> {
    if f.abs() < MAX_EXACT && f == f.trunc() {
        own(unsafe { ffi::PyLong_FromLongLong(f as i64) })
    } else {
        own(unsafe { ffi::PyFloat_FromDouble(f) })
    }
}

/// m / 10^k with Python's exactly rounded int division, for |m| beyond 2^53.
#[cold]
#[inline(never)]
fn big_decimal(m: i64, k: usize) -> R<Owned> {
    let a = own(unsafe { ffi::PyLong_FromLongLong(m) })?;
    let b = own(unsafe { ffi::PyLong_FromLongLong(POW10_INT[k]) })?;
    let q = own(unsafe { ffi::PyNumber_TrueDivide(a.ptr(), b.ptr()) })?;
    let f = unsafe { ffi::PyFloat_AsDouble(q.ptr()) };
    if f == -1.0 && !unsafe { ffi::PyErr_Occurred() }.is_null() {
        return py_fail();
    }
    make_number(f)
}

/// Copies lists and dicts recursively, charging len+1 per container. None means the budget ran out.
fn deep_copy(v: *mut ffi::PyObject, budget: &mut i64, depth: usize) -> R<Option<Owned>> {
    if depth > COPY_DEPTH_GUARD {
        return bad("nesting too deep", 0);
    }
    let ty = unsafe { ffi::Py_TYPE(v) };
    let is_list = std::ptr::eq(ty, std::ptr::addr_of_mut!(ffi::PyList_Type));
    let is_dict = std::ptr::eq(ty, std::ptr::addr_of_mut!(ffi::PyDict_Type));
    if is_list {
        let n = unsafe { ffi::PyList_Size(v) };
        *budget -= n as i64 + 1;
        if *budget < 0 {
            return Ok(None);
        }
        let out = own(unsafe { ffi::PyList_New(n) })?;
        for i in 0..n {
            let el = unsafe { ffi::PyList_GetItem(v, i) };
            if el.is_null() {
                return py_fail();
            }
            let Some(c) = deep_copy(el, budget, depth + 1)? else {
                return Ok(None);
            };
            if unsafe { ffi::PyList_SetItem(out.ptr(), i, c.into_raw()) } != 0 {
                return py_fail();
            }
        }
        return Ok(Some(out));
    }
    if is_dict {
        let n = unsafe { ffi::PyDict_Size(v) };
        *budget -= n as i64 + 1;
        if *budget < 0 {
            return Ok(None);
        }
        let out = own(unsafe { ffi::PyDict_New() })?;
        let mut p: ffi::Py_ssize_t = 0;
        let (mut k, mut el): (*mut ffi::PyObject, *mut ffi::PyObject) =
            (std::ptr::null_mut(), std::ptr::null_mut());
        while unsafe { ffi::PyDict_Next(v, &mut p, &mut k, &mut el) } != 0 {
            let Some(c) = deep_copy(el, budget, depth + 1)? else {
                return Ok(None);
            };
            if unsafe { ffi::PyDict_SetItem(out.ptr(), k, c.ptr()) } != 0 {
                return py_fail();
            }
        }
        return Ok(Some(out));
    }
    Ok(Some(Owned::from_borrowed(v)))
}
