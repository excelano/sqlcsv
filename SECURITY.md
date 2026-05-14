# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security Advisories at https://github.com/excelano/sqlcsv/security/advisories/new. If you would rather not use GitHub, email david.anderson@excelano.com instead. I aim to respond within seven days.

Please do not open public issues for security problems.

## Supported versions

The latest v1.x release receives security fixes. Older versions are not supported.

## What sqlcsv can access

sqlcsv is a CLI that runs locally on your machine. It reads the CSV file you point it at, holds it in memory for the duration of the session, and writes the modified file back when you commit a write statement. It does not make network calls of any kind, has no auth layer, and does not implement administrative operations. It can only read and write files your operating-system user already has access to.

## What sqlcsv stores

sqlcsv stores command history at `~/.config/sqlcsv/history` with file mode 0600 (directory mode 0700). That is everything: no telemetry, no analytics, no remote logging.

## Verifying releases

Every GitHub release includes a `checksums.txt` file listing SHA-256 hashes of all binary archives. Verify any download before running it:

    sha256sum sqlcsv_1.0.0_linux_amd64.tar.gz
    # compare against the value in checksums.txt

Release artifacts are built by GitHub Actions from a tagged commit using the goreleaser configuration in this repo. The workflow and build configuration are public and auditable.
