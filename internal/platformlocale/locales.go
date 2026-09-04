package platformlocale

const Default = "en"

var supported = [...]string{
	"en",
	"ta",
	"hi",
	"te",
	"kn",
	"ml",
	"mr",
	"bn",
	"gu",
}

func All() []string {
	return append([]string(nil), supported[:]...)
}

func IsSupported(value string) bool {
	for _, locale := range supported {
		if value == locale {
			return true
		}
	}
	return false
}

func Resolve(requested, fallback string, available []string) string {
	if IsSupported(requested) && contains(available, requested) {
		return requested
	}
	if IsSupported(fallback) && contains(available, fallback) {
		return fallback
	}
	return Default
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
