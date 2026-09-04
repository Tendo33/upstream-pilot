# Security

Upstream Pilot stores management credentials and can modify upstream account parameters. Treat access to the application, its database and its master key as administrative access.

Defaults restrict the listener to loopback, block private upstream addresses and reject redirects in external clients. Configure HTTPS and secure cookies before exposing a deployment. Initialize the administrator over a trusted connection. Back up the encryption key with the database; it cannot be reconstructed from ciphertext.

Do not include credentials or private account exports in public issues. For a security concern, use GitHub private vulnerability reporting when enabled for this repository. The review in `docs/REVIEW.md` distinguishes verified findings from untested risks; it is not a security certification.
