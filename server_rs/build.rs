fn main() -> Result<(), Box<dyn std::error::Error>> {
    buffa_build::Config::new()
        .files(&["proto/cache_v2.proto", "proto/cache.proto"])
        .includes(&["proto/"])
        .lazy_views(true)
        .idiomatic_field_names(true)
        .compile()
        .unwrap();

    println!("cargo:rerun-if-changed=proto/cache_v2.proto");
    println!("cargo:rerun-if-changed=proto/cache_v1.proto");

    Ok(())
}
