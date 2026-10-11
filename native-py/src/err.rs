//! Decode failures.
use pyo3::exceptions::PyValueError;
use pyo3::prelude::*;

pyo3::create_exception!(jaro_native, JaroError, PyValueError, "Invalid JARO input.");

/// Why a decode stopped.
pub enum Fail {
    /// Malformed input, with the byte offset of the problem.
    Bad(String, usize),
    /// A Python exception is already set (for example MemoryError).
    Py,
}

pub type R<T> = Result<T, Fail>;

#[cold]
#[inline(never)]
pub fn bad<T>(msg: &str, off: usize) -> R<T> {
    Err(Fail::Bad(msg.to_owned(), off))
}

#[cold]
#[inline(never)]
pub fn py_fail<T>() -> R<T> {
    Err(Fail::Py)
}

/// Converts a failure into the Python exception to raise.
pub fn to_pyerr(py: Python<'_>, f: Fail) -> PyErr {
    match f {
        Fail::Py => PyErr::take(py).unwrap_or_else(|| {
            pyo3::exceptions::PySystemError::new_err("jaro_native: error without exception")
        }),
        Fail::Bad(msg, off) => {
            let e = JaroError::new_err(format!("jaro: offset {off}: {msg}"));
            let v = e.value(py);
            let _ = v.setattr("message", msg);
            let _ = v.setattr("offset", off);
            e
        }
    }
}
