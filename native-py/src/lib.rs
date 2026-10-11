//! Native decoder for the JARO binary format.
mod decode;
mod err;
mod pyo;

use pyo3::ffi;
use pyo3::prelude::*;
use pyo3::types::PyBytes;

use crate::err::{to_pyerr, JaroError};

/// Decodes JARO binary into dict/list/str/int/float/bool/None.
#[pyfunction]
fn loads_binary(py: Python<'_>, data: &Bound<'_, PyAny>) -> PyResult<Py<PyAny>> {
    if let Ok(b) = data.cast::<PyBytes>() {
        return run(py, b.as_bytes());
    }
    // bytearray / memoryview: copy through bytes(), like the pure decoder.
    let copy = unsafe { ffi::PyBytes_FromObject(data.as_ptr()) };
    if copy.is_null() {
        return Err(PyErr::fetch(py));
    }
    let copy = unsafe { Bound::from_owned_ptr(py, copy) };
    run(py, copy.cast::<PyBytes>()?.as_bytes())
}

fn run(py: Python<'_>, data: &[u8]) -> PyResult<Py<PyAny>> {
    match decode::decode(data) {
        Ok(v) => Ok(unsafe { Bound::from_owned_ptr(py, v.into_raw()) }.unbind()),
        Err(f) => Err(to_pyerr(py, f)),
    }
}

#[pymodule]
fn jaro_native(m: &Bound<'_, PyModule>) -> PyResult<()> {
    m.add_function(wrap_pyfunction!(loads_binary, m)?)?;
    m.add("JaroError", m.py().get_type::<JaroError>())?;
    Ok(())
}
