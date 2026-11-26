.PHONY: check-js test-go test-py test desktop app install

check-js:
	node --check desktop/dist/app.js

test-go:
	cd desktop && go test -v ./...

test-py:
	@tmp_home=$$(mktemp -d); \
	PYTHONDONTWRITEBYTECODE=1 AGYP_HOME="$$tmp_home" AGYP_BIN="$(CURDIR)/bin/agyp" python3 tools/tests/agyp_gateway_hardening_test.py --only t8,t13,t26,t27,t28; \
	rc=$$?; rm -rf "$$tmp_home"; exit $$rc

test: check-js test-go test-py

desktop:
	cd desktop && go build -o bin/agyp-desktop .

app:
	bash ./scripts/package-macos.sh --arch native

install:
	./scripts/install.sh
