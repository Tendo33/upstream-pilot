-- Product identity migration; password material and bcrypt hash stay unchanged.
UPDATE users SET password_hash = '$pilot-sha256$' || substring(password_hash FROM 14)
WHERE left(password_hash,13) = '$s2am-sha256$';
