//! Thin RAII layer over the raw CPython C API (abi3-safe calls only).
use pyo3::ffi;

/// An owned strong reference. Dropping it releases the reference.
pub struct Owned(*mut ffi::PyObject);

impl Owned {
    /// Wraps a new reference; returns None when the call that produced it failed.
    #[inline(always)]
    pub fn new(p: *mut ffi::PyObject) -> Option<Owned> {
        if p.is_null() {
            None
        } else {
            Some(Owned(p))
        }
    }

    /// Takes a new strong reference to a borrowed object.
    #[inline(always)]
    pub fn from_borrowed(p: *mut ffi::PyObject) -> Owned {
        unsafe { ffi::Py_INCREF(p) };
        Owned(p)
    }

    #[inline(always)]
    pub fn ptr(&self) -> *mut ffi::PyObject {
        self.0
    }

    /// Gives up ownership without releasing the reference.
    #[inline(always)]
    pub fn into_raw(self) -> *mut ffi::PyObject {
        let p = self.0;
        std::mem::forget(self);
        p
    }

    /// Returns a fresh strong reference to the same object.
    #[inline(always)]
    pub fn new_ref(&self) -> *mut ffi::PyObject {
        unsafe { ffi::Py_INCREF(self.0) };
        self.0
    }
}

impl Drop for Owned {
    #[inline(always)]
    fn drop(&mut self) {
        unsafe { ffi::Py_DECREF(self.0) };
    }
}

#[inline(always)]
pub fn none() -> *mut ffi::PyObject {
    unsafe {
        let p = ffi::Py_None();
        ffi::Py_INCREF(p);
        p
    }
}

#[inline(always)]
pub fn boolean(b: bool) -> *mut ffi::PyObject {
    unsafe {
        let p = if b { ffi::Py_True() } else { ffi::Py_False() };
        ffi::Py_INCREF(p);
        p
    }
}
