module k9o.dev/sdk-go/kibo

go 1.22

require k9o.dev/sdk-go v1.5.2

// Local development in this repository; consumers resolve the published version.
replace k9o.dev/sdk-go => ../

// v1.5.0 was published without the native static archives (lib/<os>_<arch>/), so it does not link.
retract v1.5.0
