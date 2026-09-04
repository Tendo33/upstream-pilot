[CmdletBinding()]
param(
    [string]$PostgresAdminUrl = $env:S2AM_QA_POSTGRES_ADMIN_URL,
    [string]$MockBaseUrl = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($PostgresAdminUrl)) {
    $PostgresAdminUrl = "postgres://s2amtest@127.0.0.1:55432/postgres?sslmode=disable"
}

$ProjectRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$RunID = [Guid]::NewGuid().ToString("N")
$DatabaseName = "s2am_qa_$RunID"
$TempRoot = Join-Path ([IO.Path]::GetTempPath()) "s2am-go-qa-$RunID"
$IsWindowsHost = [Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT
$ExecutableSuffix = if ($IsWindowsHost) { ".exe" } else { "" }
$AppBinary = Join-Path $TempRoot "s2am-go$ExecutableSuffix"
$MockBinary = Join-Path $TempRoot "fake-sub2api$ExecutableSuffix"
$AppStdout = Join-Path $TempRoot "app.stdout.log"
$AppStderr = Join-Path $TempRoot "app.stderr.log"
$MockStdout = Join-Path $TempRoot "mock.stdout.log"
$MockStderr = Join-Path $TempRoot "mock.stderr.log"
$AuditLogDir = Join-Path $TempRoot "logs"
$Migration1Stdout = Join-Path $TempRoot "migration-1.stdout.log"
$Migration1Stderr = Join-Path $TempRoot "migration-1.stderr.log"
$Migration2Stdout = Join-Path $TempRoot "migration-2.stdout.log"
$Migration2Stderr = Join-Path $TempRoot "migration-2.stderr.log"
$AppProcess = $null
$MockProcess = $null
$DatabaseCreated = $false
$Sessions = New-Object System.Collections.ArrayList
$RunSucceeded = $false

function Write-Step {
    param([string]$Message)
    Write-Host "[QA] $Message"
}

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) {
        throw "assertion failed: $Message"
    }
}

function Assert-Equal {
    param($Expected, $Actual, [string]$Message)
    if ($Expected -ne $Actual) {
        throw "assertion failed: $Message (expected=$Expected, actual=$Actual)"
    }
}

function Assert-Near {
    param([double]$Expected, [double]$Actual, [string]$Message, [double]$Tolerance = 0.0000001)
    if ([Math]::Abs($Expected - $Actual) -gt $Tolerance) {
        throw "assertion failed: $Message (expected=$Expected, actual=$Actual)"
    }
}

function Assert-Count {
    param([int]$Expected, $Value, [string]$Message)
    $Actual = @($Value).Count
    if ($Expected -ne $Actual) {
        throw "assertion failed: $Message (expected count=$Expected, actual count=$Actual)"
    }
}

function Get-FreeLoopbackPort {
    $Listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $Listener.Start()
    try {
        return ([Net.IPEndPoint]$Listener.LocalEndpoint).Port
    }
    finally {
        $Listener.Stop()
    }
}

function Invoke-PsqlScalar {
    param([string]$ConnectionUrl, [string]$Sql)
    $Output = & psql $ConnectionUrl -X -A -t -v ON_ERROR_STOP=1 -c $Sql
    if ($LASTEXITCODE -ne 0) {
        throw "psql failed with exit code $LASTEXITCODE"
    }
    return (($Output | Out-String).Trim())
}

function Invoke-PsqlCommand {
    param([string]$ConnectionUrl, [string]$Sql)
    & psql $ConnectionUrl -X -A -t -q -v ON_ERROR_STOP=1 -c $Sql
    if ($LASTEXITCODE -ne 0) {
        throw "psql failed with exit code $LASTEXITCODE"
    }
}

function New-DatabaseUrl {
    param([string]$AdminUrl, [string]$Name)
    try {
        $Builder = [UriBuilder]$AdminUrl
    }
    catch {
        throw "PostgresAdminUrl must be a postgres:// or postgresql:// URL"
    }
    if ($Builder.Scheme -ne "postgres" -and $Builder.Scheme -ne "postgresql") {
        throw "PostgresAdminUrl must use the postgres or postgresql scheme"
    }
    $Builder.Path = "/$Name"
    return $Builder.Uri.AbsoluteUri
}

function Start-ConfiguredProcess {
    param(
        [string]$FilePath,
        [hashtable]$Environment,
        [string]$StdoutPath,
		[string]$StderrPath,
		[string[]]$ArgumentList = @()
    )
    $Previous = @{}
    foreach ($Name in $Environment.Keys) {
        $Previous[$Name] = [Environment]::GetEnvironmentVariable($Name, "Process")
        [Environment]::SetEnvironmentVariable($Name, [string]$Environment[$Name], "Process")
    }
    try {
		$Parameters = @{
			FilePath = $FilePath
			WorkingDirectory = $ProjectRoot
			WindowStyle = "Hidden"
			PassThru = $true
			RedirectStandardOutput = $StdoutPath
			RedirectStandardError = $StderrPath
		}
		if ($ArgumentList.Count -gt 0) {
			$Parameters.ArgumentList = $ArgumentList
		}
		return Start-Process @Parameters
    }
    finally {
        foreach ($Name in $Environment.Keys) {
            [Environment]::SetEnvironmentVariable($Name, $Previous[$Name], "Process")
        }
    }
}

function New-HTTPClientSession {
    Add-Type -AssemblyName System.Net.Http
    $Cookies = [Net.CookieContainer]::new()
    $Handler = [Net.Http.HttpClientHandler]::new()
    $Handler.UseCookies = $true
    $Handler.CookieContainer = $Cookies
    $Client = [Net.Http.HttpClient]::new($Handler)
    $Client.Timeout = [TimeSpan]::FromSeconds(70)
    $Session = [PSCustomObject]@{
        Client  = $Client
        Handler = $Handler
        Cookies = $Cookies
    }
    [void]$Sessions.Add($Session)
    return $Session
}

function Invoke-HTTPJSON {
    param(
        [Parameter(Mandatory = $true)]$Session,
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Url,
        $Body = $null,
        [hashtable]$Headers = @{},
        [int[]]$ExpectedStatus = @(200)
    )
    $Request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::new($Method), [Uri]$Url)
    try {
        if ($null -ne $Body) {
            $Encoded = $Body | ConvertTo-Json -Depth 20 -Compress
            $Request.Content = [Net.Http.StringContent]::new($Encoded, [Text.Encoding]::UTF8, "application/json")
        }
        foreach ($Name in $Headers.Keys) {
            if (-not $Request.Headers.TryAddWithoutValidation([string]$Name, [string]$Headers[$Name])) {
                throw "could not add HTTP header $Name"
            }
        }
        $Response = $Session.Client.SendAsync($Request).GetAwaiter().GetResult()
        try {
            $Status = [int]$Response.StatusCode
            $Raw = $Response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            if ($ExpectedStatus -notcontains $Status) {
                throw "HTTP $Method $Url returned $Status; expected $($ExpectedStatus -join ','); body=$Raw"
            }
            $JSON = $null
            if (-not [string]::IsNullOrWhiteSpace($Raw)) {
                try {
                    $JSON = $Raw | ConvertFrom-Json
                }
                catch {
                    throw "HTTP $Method $Url returned invalid JSON: $Raw"
                }
            }
            return [PSCustomObject]@{ Status = $Status; JSON = $JSON; Raw = $Raw }
        }
        finally {
            $Response.Dispose()
        }
    }
    finally {
        $Request.Dispose()
    }
}

function Wait-HTTPReady {
    param(
        [string]$Url,
        [Diagnostics.Process]$Process = $null,
        [int]$TimeoutSeconds = 60
    )
    $Deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $Session = New-HTTPClientSession
    while ([DateTime]::UtcNow -lt $Deadline) {
        if ($null -ne $Process -and $Process.HasExited) {
            throw "process $($Process.Id) exited before $Url became ready (exit code $($Process.ExitCode))"
        }
        try {
            [void](Invoke-HTTPJSON -Session $Session -Method GET -Url $Url -ExpectedStatus @(200))
            return
        }
        catch {
            Start-Sleep -Milliseconds 250
        }
    }
    throw "timed out waiting for $Url"
}

function Get-CSRFToken {
    param($Session, [string]$BaseUrl)
    $Cookie = $Session.Cookies.GetCookies([Uri]$BaseUrl)["s2am_csrf"]
    if ($null -eq $Cookie -or [string]::IsNullOrWhiteSpace($Cookie.Value)) {
        throw "authenticated session does not contain the s2am_csrf cookie"
    }
    return $Cookie.Value
}

function Invoke-AppAPI {
    param(
        [Parameter(Mandatory = $true)]$Session,
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Path,
        $Body = $null,
        [int[]]$ExpectedStatus = @(200),
        [hashtable]$AdditionalHeaders = @{},
        [switch]$Unprotected
    )
    $Headers = @{}
    foreach ($Name in $AdditionalHeaders.Keys) {
        $Headers[$Name] = $AdditionalHeaders[$Name]
    }
    if (-not $Unprotected -and $Method -notin @("GET", "HEAD", "OPTIONS")) {
        $Headers["X-CSRF-Token"] = Get-CSRFToken -Session $Session -BaseUrl $script:AppBaseUrl
    }
    return Invoke-HTTPJSON -Session $Session -Method $Method -Url "$script:AppBaseUrl/api/v1$Path" `
        -Body $Body -Headers $Headers -ExpectedStatus $ExpectedStatus
}

function Start-AppProbeRequest {
    param(
        [Parameter(Mandatory = $true)]$Session,
        [Parameter(Mandatory = $true)][string]$AccountID
    )
    $Request = [Net.Http.HttpRequestMessage]::new(
        [Net.Http.HttpMethod]::Post,
        [Uri]"$script:AppBaseUrl/api/v1/accounts/$AccountID/probe"
    )
    $CSRFToken = Get-CSRFToken -Session $Session -BaseUrl $script:AppBaseUrl
    if (-not $Request.Headers.TryAddWithoutValidation("X-CSRF-Token", $CSRFToken)) {
        $Request.Dispose()
        throw "could not add CSRF header to asynchronous probe request"
    }
    return [PSCustomObject]@{
        Request = $Request
        Future  = $Session.Client.SendAsync($Request)
    }
}

function Receive-AppProbeRequest {
    param([Parameter(Mandatory = $true)]$Pending)
    try {
        $Response = $Pending.Future.GetAwaiter().GetResult()
        try {
            $Status = [int]$Response.StatusCode
            $Raw = $Response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            if ($Status -ne 200) {
                throw "asynchronous account probe returned HTTP $Status; body=$Raw"
            }
            return ($Raw | ConvertFrom-Json)
        }
        finally {
            $Response.Dispose()
        }
    }
    finally {
        $Pending.Request.Dispose()
    }
}

function Get-FakeProbeRequestCount {
    param(
        [Parameter(Mandatory = $true)]$Session,
        [Parameter(Mandatory = $true)][string]$BaseUrl,
        [Parameter(Mandatory = $true)][int64]$RemoteID
    )
    $Stats = (Invoke-HTTPJSON -Session $Session -Method GET -Url "$BaseUrl/control/stats").JSON
    $Property = $Stats.probe_requests.PSObject.Properties[[string]$RemoteID]
    if ($null -eq $Property) {
        return 0
    }
    return [int]$Property.Value
}

function Wait-FakeProbeRequestCount {
    param(
        [Parameter(Mandatory = $true)]$Session,
        [Parameter(Mandatory = $true)][string]$BaseUrl,
        [Parameter(Mandatory = $true)][int64]$RemoteID,
        [Parameter(Mandatory = $true)][int]$Minimum
    )
    $Deadline = [DateTime]::UtcNow.AddSeconds(5)
    while ([DateTime]::UtcNow -lt $Deadline) {
        if ((Get-FakeProbeRequestCount -Session $Session -BaseUrl $BaseUrl -RemoteID $RemoteID) -ge $Minimum) {
            return
        }
        Start-Sleep -Milliseconds 25
    }
    throw "timed out waiting for fake probe request $Minimum for remote account $RemoteID"
}

function Wait-AccountBalanceSnapshot {
    param(
        [Parameter(Mandatory = $true)]$Session,
        [Parameter(Mandatory = $true)][string]$AccountID
    )
    $Deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ([DateTime]::UtcNow -lt $Deadline) {
        $Response = Invoke-AppAPI -Session $Session -Method POST -Path "/accounts/balances" -Body @{
            account_ids = @($AccountID)
        }
        $Rows = @($Response.JSON.data)
        if ($Rows.Count -eq 1 -and [string]$Rows[0].status -ne "pending") {
            return $Response
        }
        Start-Sleep -Milliseconds 100
    }
    throw "timed out waiting for account balance snapshot $AccountID"
}

function New-AccountSettings {
    param([hashtable]$Overrides = @{})
    $Settings = [ordered]@{
        health_enabled            = $false
        probe_interval_seconds    = 30
        probe_timeout_seconds     = 7
        failure_threshold         = 2
        recovery_success_threshold = 1
        probe_model               = "test-model"
        rate_sync_enabled         = $false
        rate_sync_interval_seconds = 30
        source_type               = "sub2api"
        source_type_locked        = $false
        source_base_url           = ""
        source_credential         = ""
        clear_source_credential   = $false
        source_user_id            = ""
        source_group              = ""
        recharge_ratio            = 1.0
        priority_enabled          = $false
        guard_enabled             = $false
        guard_operator            = "gte"
        guard_priority            = 999
    }
    foreach ($Name in $Overrides.Keys) {
        if (-not $Settings.Contains($Name)) {
            throw "unknown account setting override: $Name"
        }
        $Settings[$Name] = $Overrides[$Name]
    }
    return $Settings
}

function Get-AccountByRemoteID {
    param($Accounts, [int64]$RemoteID)
    $Matches = @($Accounts | Where-Object { [int64]$_.remote_id -eq $RemoteID })
    Assert-Count 1 $Matches "remote account $RemoteID must exist exactly once"
    return $Matches[0]
}

function Get-AppAccounts {
    param($Session)
    $Response = Invoke-AppAPI -Session $Session -Method GET -Path "/accounts"
    return @($Response.JSON.data)
}

function Stop-QAProcess {
    param([Diagnostics.Process]$Process)
    if ($null -eq $Process) {
        return
    }
    try {
        if (-not $Process.HasExited) {
            $Process.Kill()
            [void]$Process.WaitForExit(5000)
        }
    }
    catch {
        Write-Warning "could not stop process $($Process.Id): $($_.Exception.Message)"
    }
    finally {
        $Process.Dispose()
    }
}

function Show-LogTail {
    param([string]$Name, [string]$Path)
    if (Test-Path -LiteralPath $Path) {
        Write-Warning "$Name log tail:`n$((Get-Content -LiteralPath $Path -Tail 80) -join [Environment]::NewLine)"
    }
}

try {
    foreach ($Command in @("go", "psql")) {
        if ($null -eq (Get-Command $Command -ErrorAction SilentlyContinue)) {
            throw "required command is not available: $Command"
        }
    }

    Write-Step "validating PostgreSQL administrative connection"
    $AdminDatabase = Invoke-PsqlScalar -ConnectionUrl $PostgresAdminUrl -Sql "SELECT current_database()"
    if ($AdminDatabase -notin @("postgres", "template1")) {
        throw "refusing to use PostgreSQL admin URL for database '$AdminDatabase'; use postgres or template1"
    }
    $AppDatabaseUrl = New-DatabaseUrl -AdminUrl $PostgresAdminUrl -Name $DatabaseName

    New-Item -ItemType Directory -Path $TempRoot | Out-Null
    Invoke-PsqlCommand -ConnectionUrl $PostgresAdminUrl -Sql "CREATE DATABASE `"$DatabaseName`""
    $DatabaseCreated = $true
    Write-Step "created isolated database $DatabaseName"

    Write-Step "building application and QA upstream fixture"
    Push-Location $ProjectRoot
    try {
        & go build -trimpath -o $AppBinary ./cmd/s2am-go
        if ($LASTEXITCODE -ne 0) { throw "application build failed" }
        if ([string]::IsNullOrWhiteSpace($MockBaseUrl)) {
            & go build -trimpath -o $MockBinary ./qa/fake-sub2api
            if ($LASTEXITCODE -ne 0) { throw "mock upstream build failed" }
        }
    }
    finally {
        Pop-Location
    }

	Write-Step "checking concurrent database migration startup"
	$MigrationEnvironment = @{
		S2AM_DATABASE_URL = $AppDatabaseUrl
		S2AM_MASTER_KEY = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
		S2AM_AUTO_MIGRATE = "true"
		S2AM_LOG_DIR = $AuditLogDir
	}
	$Migration1 = $null
	$Migration2 = $null
	try {
		$Migration1 = Start-ConfiguredProcess -FilePath $AppBinary -Environment $MigrationEnvironment `
			-StdoutPath $Migration1Stdout -StderrPath $Migration1Stderr -ArgumentList @("-migrate-only")
		$Migration2 = Start-ConfiguredProcess -FilePath $AppBinary -Environment $MigrationEnvironment `
			-StdoutPath $Migration2Stdout -StderrPath $Migration2Stderr -ArgumentList @("-migrate-only")
		$Migration1.WaitForExit()
		$Migration2.WaitForExit()
		$Migration1Output = [IO.File]::ReadAllText($Migration1Stdout)
		$Migration2Output = [IO.File]::ReadAllText($Migration2Stdout)
		Assert-True ($Migration1Output -match 'migrations applied') "first concurrent migration process"
		Assert-True ($Migration2Output -match 'migrations applied') "second concurrent migration process"
		$AppliedMigrations = Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM schema_migrations"
		$ExpectedMigrations = @(Get-ChildItem -LiteralPath (Join-Path $ProjectRoot "internal/database/migrations") -File -Filter "*.sql").Count
		Assert-Equal $ExpectedMigrations ([int]$AppliedMigrations) "all migrations must be applied exactly once"
	}
	finally {
		Stop-QAProcess -Process $Migration1
		Stop-QAProcess -Process $Migration2
	}

    $ControlSession = New-HTTPClientSession
    if ([string]::IsNullOrWhiteSpace($MockBaseUrl)) {
        $MockPort = Get-FreeLoopbackPort
        $MockBaseUrl = "http://127.0.0.1:$MockPort"
        $MockProcess = Start-ConfiguredProcess -FilePath $MockBinary `
            -Environment @{ S2AM_FAKE_LISTEN_ADDR = "127.0.0.1:$MockPort" } `
            -StdoutPath $MockStdout -StderrPath $MockStderr
        Wait-HTTPReady -Url "$MockBaseUrl/healthz" -Process $MockProcess
        Write-Step "started mock upstream at $MockBaseUrl"
    }
    else {
        $MockUri = [Uri]$MockBaseUrl
        $Loopback = $MockUri.Host -eq "localhost"
        $ParsedAddress = $null
        if ([Net.IPAddress]::TryParse($MockUri.Host, [ref]$ParsedAddress)) {
            $Loopback = [Net.IPAddress]::IsLoopback($ParsedAddress)
        }
        if ($MockUri.Scheme -ne "http" -or -not $Loopback) {
            throw "MockBaseUrl must be an HTTP loopback URL because the fixture exposes control endpoints"
        }
        $MockBaseUrl = $MockBaseUrl.TrimEnd("/")
        Wait-HTTPReady -Url "$MockBaseUrl/healthz"
        Write-Step "reusing mock upstream at $MockBaseUrl"
    }
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/reset")

    $AppPort = Get-FreeLoopbackPort
    $script:AppBaseUrl = "http://127.0.0.1:$AppPort"
    $AppEnvironment = @{
        S2AM_DATABASE_URL            = $AppDatabaseUrl
        S2AM_LOG_DIR                 = $AuditLogDir
        S2AM_LISTEN_ADDR             = "127.0.0.1:$AppPort"
        S2AM_PUBLIC_URL              = $script:AppBaseUrl
        S2AM_MASTER_KEY              = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
        S2AM_COOKIE_SECURE           = "false"
        S2AM_AUTO_MIGRATE            = "true"
        S2AM_WORKERS                 = "1"
        S2AM_ALLOW_PRIVATE_UPSTREAMS = "true"
    }
    $AppProcess = Start-ConfiguredProcess -FilePath $AppBinary -Environment $AppEnvironment -StdoutPath $AppStdout -StderrPath $AppStderr
    Wait-HTTPReady -Url "$script:AppBaseUrl/readyz" -Process $AppProcess -TimeoutSeconds 90
    Write-Step "started isolated application at $script:AppBaseUrl"
    $RetiredAuditTables = Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT (to_regclass('public.audit_events') IS NULL)::text || '|' || (to_regclass('public.audit_events_legacy') IS NULL)::text"
    Assert-Equal "true|true" $RetiredAuditTables "PostgreSQL audit tables must be retired after startup migration"

    $AdminSession = New-HTTPClientSession
    $AdminEmail = "admin-$RunID@example.test"
    $AdminPassword = "qa-admin-password-2026"
    $Setup = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/setup" -Unprotected `
        -Body @{ email = $AdminEmail; password = $AdminPassword } -ExpectedStatus @(201)
    Assert-Equal "admin" $Setup.JSON.data.role "the first user must be an administrator"
    [void](Get-CSRFToken -Session $AdminSession -BaseUrl $script:AppBaseUrl)

    Write-Step "checking tenant balance alert settings and encrypted webhook storage"
    $DefaultBalanceAlert = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/settings/balance-alert"
    Assert-True (-not [bool]$DefaultBalanceAlert.JSON.data.enabled) "balance alerts must default to disabled"
    Assert-Near 10 ([double]$DefaultBalanceAlert.JSON.data.threshold) "default balance alert threshold"
    Assert-Equal 21600 ([int]$DefaultBalanceAlert.JSON.data.cooldown_seconds) "default balance alert cooldown"
    Assert-True (-not [bool]$DefaultBalanceAlert.JSON.data.webhook_configured) "balance alert webhook must default to unset"

    $MissingBalanceWebhook = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/settings/balance-alert" -ExpectedStatus @(400) -Body @{
        enabled = $true
        threshold = 80
        cooldown_seconds = 300
        webhook_url = ""
        clear_webhook = $false
    }
    Assert-Equal "WEBHOOK_REQUIRED" $MissingBalanceWebhook.JSON.error.code "enabled balance alerts require a webhook"
    $InvalidBalanceWebhook = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/settings/balance-alert" -ExpectedStatus @(400) -Body @{
        enabled = $false
        threshold = 80
        cooldown_seconds = 300
        webhook_url = "https://example.test/cgi-bin/webhook/send?key=must-not-be-accepted"
        clear_webhook = $false
    }
    Assert-Equal "INVALID_WECOM_WEBHOOK" $InvalidBalanceWebhook.JSON.error.code "balance alerts must only accept Enterprise WeChat webhooks"

    $BalanceWebhookKey = "qa-webhook-key-$RunID"
    $SavedBalanceAlert = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/settings/balance-alert" -Body @{
        enabled = $false
        threshold = 80
        cooldown_seconds = 300
        webhook_url = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=$BalanceWebhookKey"
        clear_webhook = $false
    }
    Assert-True ([bool]$SavedBalanceAlert.JSON.data.webhook_configured) "saved balance alert webhook state"
    Assert-Near 80 ([double]$SavedBalanceAlert.JSON.data.threshold) "saved balance alert threshold"
    Assert-True (-not $SavedBalanceAlert.Raw.Contains($BalanceWebhookKey)) "balance alert API must not echo the webhook key"
    $StoredBalanceWebhook = Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT webhook_url_ciphertext FROM balance_alert_settings WHERE owner_id='$($Setup.JSON.data.id)'"
    Assert-True ($StoredBalanceWebhook.StartsWith("v1:")) "balance alert webhook must use versioned encryption"
    Assert-True (-not $StoredBalanceWebhook.Contains($BalanceWebhookKey)) "balance alert webhook key must not be stored in plaintext"

    $ClearedBalanceAlert = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/settings/balance-alert" -Body @{
        enabled = $false
        threshold = 80
        cooldown_seconds = 300
        webhook_url = ""
        clear_webhook = $true
    }
    Assert-True (-not [bool]$ClearedBalanceAlert.JSON.data.webhook_configured) "balance alert webhook clear"

    $SitePayload = @{
        name                           = "QA fixture"
        base_url                       = $MockBaseUrl
        api_key                        = "test-admin-key"
        enabled                        = $false
        inventory_interval_seconds     = 300
        priority_start                 = 1
        priority_step                  = 10
        reconcile_interval_seconds     = 60
    }
    $CreatedSite = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites" -Body $SitePayload -ExpectedStatus @(201)
    $SiteID = [string]$CreatedSite.JSON.data.id
    $MissingCSRF = Invoke-HTTPJSON -Session $AdminSession -Method POST -Url "$script:AppBaseUrl/api/v1/sites/$SiteID/sync" -ExpectedStatus @(403)
    Assert-Equal "CSRF_INVALID" $MissingCSRF.JSON.error.code "unsafe requests without a CSRF token must be rejected"
    Assert-Equal 2 ([int]$CreatedSite.JSON.data.account_count) "site creation must synchronize both fixture accounts"
    $AccountInventoryResponse = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts"
	Assert-True (-not $AccountInventoryResponse.Raw.Contains("qa-export-secret-never-return")) "account API must not return exported API keys"
	Assert-True (-not $AccountInventoryResponse.Raw.Contains('"api_key"')) "account API must not expose credential fields"
	$Accounts = @($AccountInventoryResponse.JSON.data)
    Assert-Count 2 $Accounts "administrator inventory"
	$Account101 = Get-AccountByRemoteID $Accounts 101
	$Account102 = Get-AccountByRemoteID $Accounts 102
	Assert-Equal "https://source.example.test/v1" ([string]$Account101.observed_source_base_url) "export fallback observed source URL"
	Assert-Equal "$MockBaseUrl/newapi/v1" ([string]$Account102.observed_source_base_url) "OpenAI account observed NewAPI URL"
	Assert-Equal "sub2api" ([string]$Account101.source_type) "unclassified account source default"
	Assert-Equal "newapi" ([string]$Account102.source_type) "NewAPI source auto classification"
	Assert-True (-not [bool]$Account102.source_type_locked) "automatically classified source must remain unlocked"
	Assert-Equal 30 ([int]$Account102.probe_interval_seconds) "default probe interval"
	Assert-Equal 7 ([int]$Account102.probe_timeout_seconds) "default probe timeout"
	Assert-Equal 2 ([int]$Account102.failure_threshold) "default failure threshold"
	Assert-Equal 1 ([int]$Account102.recovery_success_threshold) "default recovery success threshold"
	Assert-Equal "gpt-5.5" ([string]$Account102.probe_model) "OpenAI default probe model"
	Assert-Equal 30 ([int]$Account102.rate_sync_interval_seconds) "default rate synchronization interval"
	$ObservedStats = (Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/control/stats").JSON
	Assert-True ([int]$ObservedStats.export_requests -gt 0) "omitted list credentials must use the account export fallback"

	Write-Step "checking selective bulk account settings"
	$BulkPriority = Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/accounts/bulk-settings" -Body @{
		account_ids = @([string]$Account101.id, [string]$Account102.id)
		priority = @{ enabled = $true }
	}
	Assert-Equal 2 ([int]$BulkPriority.JSON.data.updated_count) "bulk settings updated account count"
	$BulkAccounts = @($BulkPriority.JSON.data.accounts)
	Assert-Count 2 $BulkAccounts "bulk settings response accounts"
	foreach ($BulkAccount in $BulkAccounts) {
		Assert-True ([bool]$BulkAccount.priority_enabled) "selected priority settings must be applied"
		Assert-True (-not [bool]$BulkAccount.health_enabled) "omitted health settings must remain unchanged"
		Assert-True (-not [bool]$BulkAccount.rate_sync_enabled) "omitted rate sync settings must remain unchanged"
		Assert-True (-not [bool]$BulkAccount.guard_enabled) "omitted guard settings must remain unchanged"
	}
	$BulkAccount102 = Get-AccountByRemoteID $BulkAccounts 102
	Assert-Equal "gpt-5.5" ([string]$BulkAccount102.probe_model) "omitted probe model must remain unchanged"
	$BulkHealth = Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/accounts/bulk-settings" -Body @{
		account_ids = @([string]$Account101.id, [string]$Account102.id)
		health = @{
			enabled = $false
			probe_interval_seconds = 30
			probe_timeout_seconds = 7
			failure_threshold = 2
			recovery_success_threshold = 3
			probe_model = "test-model"
		}
	}
	foreach ($BulkAccount in @($BulkHealth.JSON.data.accounts)) {
		Assert-Equal 3 ([int]$BulkAccount.recovery_success_threshold) "bulk recovery success threshold"
	}
	$LegacySettings = New-AccountSettings @{ failure_threshold = 4 }
	[void]$LegacySettings.Remove("recovery_success_threshold")
	$LegacySettingsResponse = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $LegacySettings
	Assert-Equal 3 ([int]$LegacySettingsResponse.JSON.data.recovery_success_threshold) "legacy settings request must preserve the stored recovery success threshold"
	Assert-Equal 4 ([int]$LegacySettingsResponse.JSON.data.failure_threshold) "legacy settings request must still update other fields"
	$BulkHealthReset = Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/accounts/bulk-settings" -Body @{
		account_ids = @([string]$Account101.id, [string]$Account102.id)
		health = @{
			enabled = $false
			probe_interval_seconds = 30
			probe_timeout_seconds = 7
			failure_threshold = 2
			recovery_success_threshold = 1
			probe_model = "test-model"
		}
	}
	Assert-Equal 2 ([int]$BulkHealthReset.JSON.data.updated_count) "bulk health reset count"
	$BulkPriorityReset = Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/accounts/bulk-settings" -Body @{
		account_ids = @([string]$Account101.id, [string]$Account102.id)
		priority = @{ enabled = $false }
	}
	foreach ($BulkAccount in @($BulkPriorityReset.JSON.data.accounts)) {
		Assert-True (-not [bool]$BulkAccount.priority_enabled) "bulk settings must accept an explicit disabled value"
	}

	Write-Step "checking Sub2API account balance through the admin usage snapshot"
	$BalanceResponse = Wait-AccountBalanceSnapshot -Session $AdminSession -AccountID ([string]$Account101.id)
	$BalanceRows = @($BalanceResponse.JSON.data)
	Assert-Count 1 $BalanceRows "account balance result"
	Assert-Equal "ok" ([string]$BalanceRows[0].status) "Sub2API account balance status"
	Assert-Equal "sub2api-admin" ([string]$BalanceRows[0].provider) "Sub2API account balance provider"
	Assert-Equal "5h" ([string]$BalanceRows[0].plan_name) "Sub2API account balance window"
	Assert-Near 75 ([double]$BalanceRows[0].remaining) "Sub2API account remaining percentage"
	Assert-Near 25 ([double]$BalanceRows[0].used) "Sub2API account used percentage"
	Assert-Near 100 ([double]$BalanceRows[0].total) "Sub2API account total percentage"
	Assert-Equal "%" ([string]$BalanceRows[0].unit) "Sub2API account balance unit"
	Assert-True (-not $BalanceResponse.Raw.Contains("test-admin-key")) "balance API must not expose the site admin key"
	Assert-True (-not $BalanceResponse.Raw.Contains("qa-export-secret-never-return")) "balance API must not expose account credentials"
	$SnapshotCount = Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM account_balance_snapshots WHERE account_id='$($Account101.id)'"
	Assert-Equal 1 ([int]$SnapshotCount) "account balance snapshot must be persisted"
	$UsageRequestsBefore = [int]((Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/control/stats").JSON.usage_requests."101")
	$CachedBalanceResponse = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/balances" -Body @{
		account_ids = @([string]$Account101.id)
	}
	Assert-Equal "ok" ([string]@($CachedBalanceResponse.JSON.data)[0].status) "cached account balance status"
	$UsageRequestsAfter = [int]((Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/control/stats").JSON.usage_requests."101")
	Assert-Equal $UsageRequestsBefore $UsageRequestsAfter "reading a cached balance must not query the upstream again"

	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ export_available = $false })
	[void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/sync")
	$Account101 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-Equal "https://source.example.test/v1" ([string]$Account101.observed_source_base_url) "missing export endpoint must preserve the previous observation"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ export_available = $true; export_credentials_null = $true })
	[void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/sync")
	$Account101 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-True ($null -eq $Account101.observed_source_base_url) "explicit null exported credentials must clear the previous observation"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ export_credentials_null = $false })
	[void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/sync")
	$Account101 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-Equal "https://source.example.test/v1" ([string]$Account101.observed_source_base_url) "valid exported credentials must restore the observation"
	Assert-True ($null -eq $Account101.uptime_percent) "account without probes must have null uptime"
	Assert-Equal 0 ([int]$Account101.uptime_successes) "account without probes uptime successes"
	Assert-Equal 0 ([int]$Account101.uptime_total) "account without probes uptime total"
	Assert-Equal 60 ([int]$Account101.uptime_window_size) "account uptime sample window"
	Assert-Equal "" ([string]$Account101.uptime_timeline) "account without probes uptime timeline"
	$UptimeWindowSQL = @"
INSERT INTO probe_attempts(id,owner_id,site_id,account_id,kind,success,latency_ms,model,message,created_at)
SELECT ('00000000-0000-4000-8000-' || lpad(sample::text,12,'0'))::uuid,
       '$($Setup.JSON.data.id)','$SiteID','$($Account102.id)','scheduled',sample > 1,1,'uptime-window','fixture',
	   now() - (61-sample)*interval '1 minute'
FROM generate_series(1,61) sample;
"@
	Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql $UptimeWindowSQL
	$Account102 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 102
	Assert-Equal 60 ([int]$Account102.uptime_successes) "uptime must only include successful probes in the recent window"
	Assert-Equal 60 ([int]$Account102.uptime_total) "uptime must cap the recent sample window"
	Assert-Near 100 ([double]$Account102.uptime_percent) "uptime must exclude the oldest probe outside the window"
	Assert-Equal 60 ([string]$Account102.uptime_timeline).Length "uptime timeline must use the recent sample window"
	Assert-Equal ([string]::new('S', 60)) ([string]$Account102.uptime_timeline) "uptime timeline compact status order"

    Write-Step "checking legacy PostgreSQL audit export"
    Stop-QAProcess -Process $AppProcess
    $AppProcess = $null
    $LegacyEventID = [Guid]::NewGuid().ToString()
    $LegacyCreatedAt = "2026-07-01T02:03:04Z"
    $LegacySQL = @"
CREATE TABLE audit_events_legacy (
  id uuid PRIMARY KEY,
  owner_id uuid NOT NULL,
  actor_user_id uuid,
  site_id uuid,
  account_id uuid,
  action text NOT NULL,
  outcome text NOT NULL,
  detail jsonb NOT NULL,
  created_at timestamptz NOT NULL
);
INSERT INTO audit_events_legacy(id,owner_id,actor_user_id,site_id,account_id,action,outcome,detail,created_at)
VALUES('$LegacyEventID','$($Setup.JSON.data.id)','$($Setup.JSON.data.id)','$SiteID','$($Account101.id)','legacy.fixture','success',jsonb_build_object('migrated',true),'$LegacyCreatedAt');
"@
    Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql $LegacySQL
    $AppProcess = Start-ConfiguredProcess -FilePath $AppBinary -Environment $AppEnvironment -StdoutPath $AppStdout -StderrPath $AppStderr
    Wait-HTTPReady -Url "$script:AppBaseUrl/readyz" -Process $AppProcess -TimeoutSeconds 90
    $LegacyTableGone = Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT (to_regclass('public.audit_events_legacy') IS NULL)::text"
    Assert-Equal "true" $LegacyTableGone "legacy audit table must be dropped only after export"
    $LegacyEvents = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=1&page_size=200").JSON.data.items
    $MigratedEvents = @($LegacyEvents | Where-Object { $_.id -eq $LegacyEventID })
    Assert-Count 1 $MigratedEvents "legacy event export"
    Assert-Equal "QA fixture" $MigratedEvents[0].site_name "legacy site name snapshot"
    Assert-Equal "Claude primary" $MigratedEvents[0].account_name "legacy account name snapshot"
    Assert-True ([bool]$MigratedEvents[0].detail.migrated) "legacy event detail"
    Assert-Equal $LegacyCreatedAt ([DateTime]::Parse($MigratedEvents[0].created_at).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")) "legacy event timestamp"

    Write-Step "checking manual scheduling ownership"
    $ManualOff = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/scheduling" -Body @{ schedulable = $false }
    Assert-True (-not [bool]$ManualOff.JSON.data.schedulable) "manual scheduling off must persist"
    Assert-True (-not [bool]$ManualOff.JSON.data.managed_hold) "manual scheduling off must not create a managed hold"
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true })
    $ManualOffProbe = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
    Assert-True ([bool]$ManualOffProbe.JSON.data.result.success) "manual-off account probe must still run"
    Assert-True (-not [bool]$ManualOffProbe.JSON.data.account.schedulable) "successful probe must not restore a manually disabled account"
    Assert-True (-not [bool]$ManualOffProbe.JSON.data.account.managed_hold) "manual-off account must remain outside managed recovery"
    $ManualOn = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/scheduling" -Body @{ schedulable = $true }
    Assert-True ([bool]$ManualOn.JSON.data.schedulable) "manual scheduling on must persist"
    Assert-True (-not [bool]$ManualOn.JSON.data.managed_hold) "manual scheduling on must clear managed hold ownership"

    Write-Step "checking failure pause and success restore"
	$HealthSettings = New-AccountSettings @{ health_enabled = $true; failure_threshold = 2; recovery_success_threshold = 2 }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $HealthSettings)
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{
        probe_success = $false
        probe_failure = @{
            type = "test_complete"
            status = "failed"
            success = $false
            error = @{
                status = 403
                code = "INSUFFICIENT_BALANCE"
                message = "Insufficient account balance"
            }
        }
    })
    $FirstFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
    Assert-True (-not [bool]$FirstFailure.JSON.data.result.success) "first probe must fail"
    Assert-True (-not [bool]$FirstFailure.JSON.data.result.paused) "first failure must not pause before threshold"
    Assert-Equal "BALANCE" $FirstFailure.JSON.data.result.failure_reason "probe result balance classification"
    Assert-Equal 403 ([int]$FirstFailure.JSON.data.result.http_status) "probe result HTTP status"
    Assert-Equal "BALANCE" $FirstFailure.JSON.data.account.last_failure_reason "account balance classification"
    Assert-Equal 403 ([int]$FirstFailure.JSON.data.account.last_failure_http_status) "account failure HTTP status"
    $SecondFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
    Assert-True ([bool]$SecondFailure.JSON.data.result.paused) "second consecutive failure must pause scheduling"
    Assert-True (-not [bool]$SecondFailure.JSON.data.account.schedulable) "paused account must be unschedulable"
    Assert-True ([bool]$SecondFailure.JSON.data.account.managed_hold) "pause must be marked as a managed hold"
    Assert-Equal "paused" $SecondFailure.JSON.data.account.health_state "paused health state"
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true })
	$RecoveryPending = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$RecoveryPending.JSON.data.result.success) "first recovery probe must succeed"
	Assert-True (-not [bool]$RecoveryPending.JSON.data.result.restored) "first recovery success must wait for threshold"
	Assert-True (-not [bool]$RecoveryPending.JSON.data.account.schedulable) "account must remain paused below recovery threshold"
	Assert-Equal 1 ([int]$RecoveryPending.JSON.data.account.consecutive_recovery_successes) "recovery success progress"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $false })
	$RecoveryInterrupted = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$RecoveryInterrupted.JSON.data.result.success) "failed recovery probe must interrupt the success sequence"
	Assert-Equal 0 ([int]$RecoveryInterrupted.JSON.data.account.consecutive_recovery_successes) "failed recovery probe must reset success progress"
	Assert-True ([bool]$RecoveryInterrupted.JSON.data.account.managed_hold) "failed recovery probe must retain managed hold"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true })
	$RecoveryRestarted = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$RecoveryRestarted.JSON.data.result.restored) "recovery must wait for a new consecutive sequence"
	Assert-Equal 1 ([int]$RecoveryRestarted.JSON.data.account.consecutive_recovery_successes) "restarted recovery success progress"
	$Recovered = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$Recovered.JSON.data.result.success) "threshold recovery probe must succeed"
	Assert-True ([bool]$Recovered.JSON.data.result.restored) "recovery threshold must restore scheduling"
    Assert-True ([bool]$Recovered.JSON.data.account.schedulable) "recovered account must be schedulable"
    Assert-True (-not [bool]$Recovered.JSON.data.account.managed_hold) "managed hold must clear after recovery"
    Assert-Equal "healthy" $Recovered.JSON.data.account.health_state "recovered health state"
    $RecoveredReason = $Recovered.JSON.data.account.PSObject.Properties["last_failure_reason"]
    $RecoveredHTTPStatus = $Recovered.JSON.data.account.PSObject.Properties["last_failure_http_status"]
    Assert-True ($null -eq $RecoveredReason -or $null -eq $RecoveredReason.Value) "successful probe must clear the failure classification"
    Assert-True ($null -eq $RecoveredHTTPStatus -or $null -eq $RecoveredHTTPStatus.Value) "successful probe must clear the failure HTTP status"
	Assert-Equal 4 ([int]$Recovered.JSON.data.account.uptime_successes) "recovered account uptime successes"
	Assert-Equal 7 ([int]$Recovered.JSON.data.account.uptime_total) "recovered account uptime total"
	Assert-Near 57.1 ([double]$Recovered.JSON.data.account.uptime_percent) "recovered account uptime percent"
	Assert-Equal 60 ([int]$Recovered.JSON.data.account.uptime_window_size) "recovered account uptime sample window"
	Assert-True (-not [string]::IsNullOrWhiteSpace([string]$Recovered.JSON.data.account.uptime_window_started_at)) "uptime window start"
	Assert-True (-not [string]::IsNullOrWhiteSpace([string]$Recovered.JSON.data.account.uptime_window_ended_at)) "uptime window end"
	Assert-Equal "SSFSFFS" ([string]$Recovered.JSON.data.account.uptime_timeline) "recovered account compact uptime timeline"

	Write-Step "checking stale scheduling mirrors and probe ordering"
	$ImmediateHoldSettings = New-AccountSettings @{ health_enabled = $true; failure_threshold = 1 }
	[void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $ImmediateHoldSettings)
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{
		probe_success = $false
		probe_delay_ms = 0
		schedulable = $false
	})
	Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql "UPDATE upstream_accounts SET schedulable=true,managed_hold=false,consecutive_failures=0,health_state='healthy' WHERE id='$($Account101.id)'"
	$StaleMirrorFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$StaleMirrorFailure.JSON.data.account.schedulable) "remote manual-off state must repair a stale local scheduling mirror"
	Assert-True (-not [bool]$StaleMirrorFailure.JSON.data.account.managed_hold) "an already disabled remote account must not become a managed hold"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true })
	$StaleMirrorSuccess = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$StaleMirrorSuccess.JSON.data.result.restored) "success must not restore a remote account S2AM-GO did not pause"
	Assert-True (-not [bool]$StaleMirrorSuccess.JSON.data.account.schedulable) "unowned remote manual-off state must survive a successful probe"
	[void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/scheduling" -Body @{ schedulable = $true })

	$ManualRaceStartCount = Get-FakeProbeRequestCount -Session $ControlSession -BaseUrl $MockBaseUrl -RemoteID 101
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $false; probe_delay_ms = 1200 })
	$ProbeBeforeManualIntent = Start-AppProbeRequest -Session $AdminSession -AccountID $Account101.id
	Wait-FakeProbeRequestCount -Session $ControlSession -BaseUrl $MockBaseUrl -RemoteID 101 -Minimum ($ManualRaceStartCount + 1)
	$ManualOffDuringProbe = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/scheduling" -Body @{ schedulable = $false }
	Assert-True (-not [bool]$ManualOffDuringProbe.JSON.data.schedulable) "manual scheduling intent during a probe"
	$StaleAfterManualIntent = Receive-AppProbeRequest -Pending $ProbeBeforeManualIntent
	Assert-True (-not [bool]$StaleAfterManualIntent.data.result.success) "delayed probe fixture result"
	Assert-True (-not [bool]$StaleAfterManualIntent.data.account.schedulable) "probe predating manual intent must not re-enable scheduling"
	Assert-True (-not [bool]$StaleAfterManualIntent.data.account.managed_hold) "probe predating manual intent must not claim recovery ownership"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true; probe_delay_ms = 0 })
	[void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/scheduling" -Body @{ schedulable = $true })

	$OrderingStartCount = Get-FakeProbeRequestCount -Session $ControlSession -BaseUrl $MockBaseUrl -RemoteID 101
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $false; probe_delay_ms = 1200 })
	$OlderProbe = Start-AppProbeRequest -Session $AdminSession -AccountID $Account101.id
	Wait-FakeProbeRequestCount -Session $ControlSession -BaseUrl $MockBaseUrl -RemoteID 101 -Minimum ($OrderingStartCount + 1)
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true; probe_delay_ms = 0 })
	$NewerProbe = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$NewerProbe.JSON.data.result.success) "newer probe must complete first"
	$OlderProbeResult = Receive-AppProbeRequest -Pending $OlderProbe
	Assert-True (-not [bool]$OlderProbeResult.data.result.success) "older delayed probe fixture result"
	$OrderedAccount = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-Equal "healthy" $OrderedAccount.health_state "older delayed failure must not overwrite newer success"
	Assert-Equal 0 ([int]$OrderedAccount.consecutive_failures) "older delayed failure must not increment the newer state"
	Assert-True ([bool]$OrderedAccount.schedulable) "older delayed failure must not pause scheduling"
	Assert-True (-not [bool]$OrderedAccount.managed_hold) "older delayed failure must not create recovery ownership"
	[void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $HealthSettings)

	Write-Step "checking managed hold fault recovery"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $false })
	$FaultFirstFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$FaultFirstFailure.JSON.data.result.success) "fault setup probe must fail deterministically"
	Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql "ALTER TABLE upstream_accounts ADD CONSTRAINT qa_reject_managed_hold CHECK (NOT managed_hold)"
	$BeforeClaimFailureSamples = [int](Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM probe_attempts WHERE account_id='$($Account101.id)'")
	$ClaimFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe" -ExpectedStatus @(422)
	Assert-Equal "PROBE_FAILED" $ClaimFailure.JSON.error.code "injected managed hold claim failure"
	$AfterClaimFailureSamples = [int](Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM probe_attempts WHERE account_id='$($Account101.id)'")
	Assert-Equal ($BeforeClaimFailureSamples + 1) $AfterClaimFailureSamples "probe sample must survive managed hold claim SQL failure"
	$ClaimRejectedAccount = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-True (-not [bool]$ClaimRejectedAccount.managed_hold) "failed ownership claim must not call itself managed"
	Assert-True ([bool]$ClaimRejectedAccount.schedulable) "failed ownership claim must preserve local scheduling intent"
	$RemoteAfterClaimFailure = Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/api/v1/admin/accounts/101" -Headers @{ "x-api-key" = "test-admin-key" }
	Assert-True ([bool]$RemoteAfterClaimFailure.JSON.data.schedulable) "remote pause must not happen before ownership is durable"
	Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql "ALTER TABLE upstream_accounts DROP CONSTRAINT qa_reject_managed_hold"
	$ClaimRetry = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$ClaimRetry.JSON.data.result.paused) "managed hold claim must be retryable after SQL recovery"
	Assert-True ([bool]$ClaimRetry.JSON.data.account.managed_hold) "retried managed hold claim"
	Assert-Equal "paused" $ClaimRetry.JSON.data.account.health_state "retried pause health state"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true })
	$ClaimRecovery = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$ClaimRecovery.JSON.data.result.restored) "first fault setup recovery success must wait for threshold"
	$ClaimRecoveryComplete = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$ClaimRecoveryComplete.JSON.data.result.restored) "fault setup managed pause recovery"

	$ImmediateHoldSettings = New-AccountSettings @{ health_enabled = $true; failure_threshold = 1 }
	[void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $ImmediateHoldSettings)
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $false; scheduling_failure = $true })
	$PauseControlFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True (-not [bool]$PauseControlFailure.JSON.data.result.paused) "failed remote pause must remain pending"
	Assert-True ([bool]$PauseControlFailure.JSON.data.account.managed_hold) "remote pause failure must retain durable ownership"
	Assert-True ([bool]$PauseControlFailure.JSON.data.account.schedulable) "remote pause failure keeps the last confirmed scheduling state"
	Assert-Equal "failing" $PauseControlFailure.JSON.data.account.health_state "remote pause failure pending state"
	$RemoteAfterPauseFailure = Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/api/v1/admin/accounts/101" -Headers @{ "x-api-key" = "test-admin-key" }
	Assert-True ([bool]$RemoteAfterPauseFailure.JSON.data.schedulable) "failed remote pause must leave upstream scheduling unchanged"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ scheduling_failure = $false })
	$PauseControlRetry = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$PauseControlRetry.JSON.data.result.paused) "pending remote pause must retry on the next failure"
	Assert-True (-not [bool]$PauseControlRetry.JSON.data.account.schedulable) "retried remote pause scheduling state"
	Assert-Equal "paused" $PauseControlRetry.JSON.data.account.health_state "retried remote pause health state"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true })
	$PauseControlRecovery = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$PauseControlRecovery.JSON.data.result.restored) "remote pause fault setup recovery"

	Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql "ALTER TABLE upstream_accounts ADD CONSTRAINT qa_reject_managed_pause CHECK (NOT managed_hold OR schedulable)"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $false })
	$BeforeFinalizeFailureSamples = [int](Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM probe_attempts WHERE account_id='$($Account101.id)'")
	$FinalizeFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe" -ExpectedStatus @(422)
	Assert-Equal "PROBE_FAILED" $FinalizeFailure.JSON.error.code "injected managed pause finalization failure"
	$AfterFinalizeFailureSamples = [int](Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM probe_attempts WHERE account_id='$($Account101.id)'")
	Assert-Equal ($BeforeFinalizeFailureSamples + 1) $AfterFinalizeFailureSamples "probe sample must survive pause finalization SQL failure"
	$PendingPause = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-True ([bool]$PendingPause.managed_hold) "ownership must remain durable after remote pause"
	Assert-True ([bool]$PendingPause.schedulable) "pending pause keeps the last confirmed local scheduling state"
	Assert-Equal "failing" $PendingPause.health_state "managed failing state represents a pending pause"
	$RemoteAfterFinalizeFailure = Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/api/v1/admin/accounts/101" -Headers @{ "x-api-key" = "test-admin-key" }
	Assert-True (-not [bool]$RemoteAfterFinalizeFailure.JSON.data.schedulable) "remote pause must already be applied before finalization failure"
	Invoke-PsqlCommand -ConnectionUrl $AppDatabaseUrl -Sql "ALTER TABLE upstream_accounts DROP CONSTRAINT qa_reject_managed_pause"
	$FinalizeRetry = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$FinalizeRetry.JSON.data.result.paused) "pending managed pause must finalize on the next failed probe"
	Assert-True ([bool]$FinalizeRetry.JSON.data.account.managed_hold) "finalized pause ownership"
	Assert-True (-not [bool]$FinalizeRetry.JSON.data.account.schedulable) "finalized managed pause scheduling state"
	Assert-Equal "paused" $FinalizeRetry.JSON.data.account.health_state "finalized managed pause health state"
	$RepeatedPausedFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$RepeatedPausedFailure.JSON.data.account.managed_hold) "repeated failure must retain managed ownership"
	Assert-True (-not [bool]$RepeatedPausedFailure.JSON.data.account.schedulable) "repeated failure must retain remote pause"
	Assert-Equal "paused" $RepeatedPausedFailure.JSON.data.account.health_state "repeated failure must remain paused"

	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ probe_success = $true; scheduling_failure = $true })
	$BeforeRecoveryFailureSamples = [int](Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM probe_attempts WHERE account_id='$($Account101.id)'")
	$RecoveryFailure = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe" -ExpectedStatus @(422)
	Assert-Equal "PROBE_FAILED" $RecoveryFailure.JSON.error.code "injected remote recovery failure"
	$AfterRecoveryFailureSamples = [int](Invoke-PsqlScalar -ConnectionUrl $AppDatabaseUrl -Sql "SELECT count(*) FROM probe_attempts WHERE account_id='$($Account101.id)'")
	Assert-Equal ($BeforeRecoveryFailureSamples + 1) $AfterRecoveryFailureSamples "successful model probe must survive remote recovery failure"
	$PendingRecovery = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
	Assert-True ([bool]$PendingRecovery.managed_hold) "remote recovery failure must retain managed ownership"
	Assert-True (-not [bool]$PendingRecovery.schedulable) "remote recovery failure must retain the confirmed pause"
	Assert-Equal "paused" $PendingRecovery.health_state "remote recovery failure health state"
	Assert-True ([string]$PendingRecovery.uptime_timeline -like "S*") "successful probe must appear in uptime after remote recovery failure"
	$RemoteAfterRecoveryFailure = Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/api/v1/admin/accounts/101" -Headers @{ "x-api-key" = "test-admin-key" }
	Assert-True (-not [bool]$RemoteAfterRecoveryFailure.JSON.data.schedulable) "failed remote recovery must leave upstream paused"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ scheduling_failure = $false })
	$RecoveryRetry = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/probe"
	Assert-True ([bool]$RecoveryRetry.JSON.data.result.success) "recovery retry deterministic result"
	Assert-True ([bool]$RecoveryRetry.JSON.data.result.restored) "managed recovery must retry after upstream recovers"
	Assert-True ([bool]$RecoveryRetry.JSON.data.account.schedulable) "recovery retry scheduling state"
	Assert-True (-not [bool]$RecoveryRetry.JSON.data.account.managed_hold) "recovery retry clears ownership"
	Assert-Equal "healthy" $RecoveryRetry.JSON.data.account.health_state "recovery retry health state"

    Write-Step "checking Sub2API and NewAPI rate synchronization"
    $Sub2Settings = New-AccountSettings @{
        health_enabled = $true
        rate_sync_enabled = $true
        recharge_ratio = 1.5
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $Sub2Settings)
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ billing_rate = 0.75 })
    $Sub2Rate = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/rate-sync"
    Assert-Near 0.75 ([double]$Sub2Rate.JSON.data.result.source_rate) "Sub2API source rate"
    Assert-Near 0.5 ([double]$Sub2Rate.JSON.data.result.effective_rate) "Sub2API recharge conversion"
    Assert-Near 0.5 ([double]$Sub2Rate.JSON.data.account.rate_multiplier) "Sub2API rate write-back"

    $DirectGroups = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account102.id)/source-groups" -Body @{
        source_base_url = "$MockBaseUrl/newapi-direct"
        source_credential = "test-newapi-key"
        source_user_id = "7"
    }
    $DirectItems = @($DirectGroups.JSON.data)
    Assert-Count 1 $DirectItems "root-level NewAPI groups response"
    Assert-Equal "direct" $DirectItems[0].group "root-level NewAPI group name"
    Assert-Near 0.4 ([double]$DirectItems[0].rate) "root-level NewAPI group ratio"

    $ManualSourceSettings = New-AccountSettings @{
        source_type = "sub2api"
        source_type_locked = $true
        probe_model = "gpt-5.5"
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account102.id)/settings" -Body $ManualSourceSettings)
    [void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/sync")
    $Account102 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 102
    Assert-Equal "sub2api" ([string]$Account102.source_type) "manual source type must survive automatic classification"
    Assert-True ([bool]$Account102.source_type_locked) "manual source type must be locked"

    $MissingGroupSettings = New-AccountSettings @{
        rate_sync_enabled = $true
        source_type = "newapi"
        source_base_url = "$MockBaseUrl/newapi"
        source_credential = "test-newapi-key"
        source_user_id = "7"
        source_group = ""
        recharge_ratio = 1.1
    }
    $MissingGroup = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account102.id)/settings" `
        -Body $MissingGroupSettings -ExpectedStatus @(400)
    Assert-Equal "NEWAPI_GROUP_REQUIRED" $MissingGroup.JSON.error.code "NewAPI group must be explicit"

    $NewAPISettings = New-AccountSettings @{
        rate_sync_enabled = $true
        source_type = "newapi"
        source_base_url = "$MockBaseUrl/newapi"
        source_credential = "test-newapi-key"
        source_user_id = "7"
        source_group = "codex-Plus"
        recharge_ratio = 1.1
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account102.id)/settings" -Body $NewAPISettings)
    $NewAPIRate = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account102.id)/rate-sync"
    Assert-Near 0.055 ([double]$NewAPIRate.JSON.data.result.source_rate) "NewAPI source rate"
    Assert-Near 0.05 ([double]$NewAPIRate.JSON.data.result.effective_rate) "NewAPI recharge conversion"
    Assert-Near 0.05 ([double]$NewAPIRate.JSON.data.account.rate_multiplier) "NewAPI rate write-back"
	Assert-Equal "valid" ([string]$NewAPIRate.JSON.data.account.source_credential_state) "successful NewAPI sync credential state"
	Assert-True (-not [string]::IsNullOrWhiteSpace([string]$NewAPIRate.JSON.data.account.source_credential_checked_at)) "successful NewAPI credential check timestamp"

	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/102" -Body @{ newapi_auth_valid = $false })
	$ExpiredNewAPI = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account102.id)/rate-sync" -ExpectedStatus @(422)
	Assert-Equal "NEWAPI_CREDENTIAL_INVALID" $ExpiredNewAPI.JSON.error.code "expired NewAPI session error code"
	$ExpiredNewAPIAccount = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 102
	Assert-Equal "invalid" ([string]$ExpiredNewAPIAccount.source_credential_state) "expired NewAPI session persisted state"
	Assert-True (-not [string]::IsNullOrWhiteSpace([string]$ExpiredNewAPIAccount.source_credential_checked_at)) "expired NewAPI credential check timestamp"
	$ExpiredNewAPIFilters = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts/filter-options"
	Assert-Equal 1 ([int]$ExpiredNewAPIFilters.JSON.data.invalid_source_credentials) "tenant-level invalid NewAPI credential count"
	$ExpiredNewAPIRaw = $ExpiredNewAPIAccount | ConvertTo-Json -Compress -Depth 20
	Assert-True (-not $ExpiredNewAPIRaw.Contains("test-newapi-key")) "credential state response must not expose the NewAPI credential"

	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/102" -Body @{ newapi_auth_valid = $true })
	$RecoveredNewAPI = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account102.id)/rate-sync"
	Assert-Equal "valid" ([string]$RecoveredNewAPI.JSON.data.account.source_credential_state) "recovered NewAPI credential state"
	$RecoveredNewAPIFilters = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts/filter-options"
	Assert-Equal 0 ([int]$RecoveredNewAPIFilters.JSON.data.invalid_source_credentials) "recovered NewAPI credential count"
	$RecoveredNewAPIError = $RecoveredNewAPI.JSON.data.account.PSObject.Properties["last_error"]
	Assert-True ($null -eq $RecoveredNewAPIError -or $null -eq $RecoveredNewAPIError.Value) "successful NewAPI sync clears the credential error"
	[void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/102" -Body @{ export_credentials_null = $false })
	[void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/sync")
	$Account102 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 102
	Assert-Equal "$MockBaseUrl/newapi" ([string]$Account102.source_base_url) "inventory sync must preserve the user-configured source URL"
	Assert-Equal "$MockBaseUrl/newapi/v1" ([string]$Account102.observed_source_base_url) "observed source URL must remain independent from the user-configured source URL"
	Assert-True ([bool]$Account102.source_type_locked) "configured NewAPI source must remain operator locked"

    Write-Step "checking global rate ordering and group-rate protection"
    $Sub2PrioritySettings = New-AccountSettings @{
        health_enabled = $true
        rate_sync_enabled = $true
        recharge_ratio = 1.5
        priority_enabled = $true
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $Sub2PrioritySettings)
    $NewAPIPrioritySettings = New-AccountSettings @{
        rate_sync_enabled = $true
        source_type = "newapi"
        source_base_url = "$MockBaseUrl/newapi"
        source_credential = ""
        source_user_id = "7"
        source_group = "codex-Plus"
        recharge_ratio = 1.1
        priority_enabled = $true
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account102.id)/settings" -Body $NewAPIPrioritySettings)
    $Reconcile = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/reconcile"
    Assert-Equal 2 ([int]$Reconcile.JSON.data.evaluated) "reconcile evaluation count"
    $Accounts = Get-AppAccounts -Session $AdminSession
    $Account101 = Get-AccountByRemoteID $Accounts 101
    $Account102 = Get-AccountByRemoteID $Accounts 102
    Assert-Equal 1 ([int]$Account102.priority) "lower multiplier must receive higher global priority"
    Assert-Equal 11 ([int]$Account101.priority) "higher multiplier must receive the next priority rank"

    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ billing_rate = 1.5 })
    [void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/rate-sync")
    $GuardSettings = New-AccountSettings @{
        health_enabled = $true
        rate_sync_enabled = $true
        recharge_ratio = 1.5
        priority_enabled = $true
        guard_enabled = $true
        guard_operator = "gte"
        guard_priority = 999
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $GuardSettings)
    [void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/reconcile")
    $Account101 = Get-AccountByRemoteID (Get-AppAccounts -Session $AdminSession) 101
    Assert-Near 1.0 ([double]$Account101.rate_multiplier) "guard fixture account rate"
    Assert-Near 1.0 ([double]$Account101.groups[0].rate_multiplier) "guard fixture group rate"
    Assert-True ([bool]$Account101.guard_holding) "equal group ratio must activate gte guard"
    Assert-Equal 999 ([int]$Account101.priority) "guard must raise the priority number to configured value"

    Write-Step "checking cache-rate weighted priority ordering"
    $UnguardSettings = New-AccountSettings @{
        health_enabled = $true
        rate_sync_enabled = $true
        recharge_ratio = 1.5
        priority_enabled = $true
        guard_enabled = $false
    }
    [void](Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/accounts/$($Account101.id)/settings" -Body $UnguardSettings)
    $SiteCachePayload = @{
        name                           = "QA fixture"
        base_url                       = $MockBaseUrl
        api_key                        = ""
        enabled                        = $false
        inventory_interval_seconds     = 300
        priority_start                 = 1
        priority_step                  = 10
        reconcile_interval_seconds     = 60
        cache_rate_priority_enabled    = $true
        cache_rate_window_seconds      = 1800
        rate_priority_weight           = 0
        cache_rate_priority_weight     = 1
    }
    $UpdatedSite = Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/sites/$SiteID/" -Body $SiteCachePayload
    Assert-True ([bool]$UpdatedSite.JSON.data.cache_rate_priority_enabled) "site cache-rate priority must be enabled"
    Assert-Equal 1800 ([int]$UpdatedSite.JSON.data.cache_rate_window_seconds) "site cache-rate window"
    Assert-Near 0 ([double]$UpdatedSite.JSON.data.rate_priority_weight) "rate weight can be zero"
    Assert-Near 1 ([double]$UpdatedSite.JSON.data.cache_rate_priority_weight) "cache-rate weight"
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ usage = @{ requests = 4; input_tokens = 10; output_tokens = 0; cache_creation_tokens = 0; cache_read_tokens = 90 } })
    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/102" -Body @{ usage = @{ requests = 4; input_tokens = 90; output_tokens = 0; cache_creation_tokens = 0; cache_read_tokens = 10 } })
    [void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/cache-sample")
    [void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/sites/$SiteID/reconcile")
    $Accounts = Get-AppAccounts -Session $AdminSession
    $Account101 = Get-AccountByRemoteID $Accounts 101
    $Account102 = Get-AccountByRemoteID $Accounts 102
    Assert-Near 0.9 ([double]$Account101.cache_rate) "high cache-rate account snapshot"
    Assert-Near 0.1 ([double]$Account102.cache_rate) "low cache-rate account snapshot"
    Assert-Equal 1 ([int]$Account101.priority) "higher cache rate must receive higher global priority when cache weight dominates"
    Assert-Equal 11 ([int]$Account102.priority) "lower cache rate must receive the next priority rank"
    $SiteCachePayload.cache_rate_priority_enabled = $false
    $SiteCachePayload.rate_priority_weight = 1
    [void](Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/sites/$SiteID/" -Body $SiteCachePayload)

    Write-Step "checking account platform/group filters and managed group rates"

    $FilterOptions = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts/filter-options").JSON.data
    Assert-Count 2 $FilterOptions.platforms "tenant platform filter options"
	Assert-True (@($FilterOptions.platforms) -contains "openai") "platform filter options must normalize case and surrounding whitespace"
    Assert-Count 1 $FilterOptions.groups "tenant group filter options"
    $AnthropicAccounts = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts?platform=anthropic").JSON.data
    Assert-Count 1 $AnthropicAccounts "platform account filter"
    Assert-Equal 101 ([int]$AnthropicAccounts[0].remote_id) "platform account filter result"
	$OpenAIAccounts = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts?platform=openai").JSON.data
	Assert-Count 1 $OpenAIAccounts "normalized platform account filter"
	Assert-Equal 102 ([int]$OpenAIAccounts[0].remote_id) "normalized platform account filter result"
    $GroupID = [string]$FilterOptions.groups[0].id
    $GroupedAccounts = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts?group_id=$GroupID").JSON.data
    Assert-Count 2 $GroupedAccounts "group account filter"

    $ManagedGroups = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/groups").JSON.data
    Assert-Count 1 $ManagedGroups "managed group inventory"
    Assert-Equal $GroupID ([string]$ManagedGroups[0].id) "managed group local ID"
    Assert-Equal 2 ([int]$ManagedGroups[0].member_count) "managed group member count"
    $SavedGroup = (Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/groups/$GroupID/config" -Body @{
        enabled = $true
        mode = "average"
        offset = 0.1
        expression = $null
        bindings = @([string]$Account101.id, [string]$Account102.id)
        apply = $true
    }).JSON.data
    Assert-Count 2 $SavedGroup.bindings "managed group source bindings"
    Assert-Near 0.625 ([double]$SavedGroup.rate_multiplier) "managed group average and offset"
    Assert-Near 0.625 ([double]$SavedGroup.rule.last_calculated_rate) "managed group calculated rate"
	$GroupStats = (Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/control/stats").JSON
	Assert-Equal 1 ([int]$GroupStats.group_updates) "initial managed group apply must update the upstream once"
	$NoOpGroupApply = (Invoke-AppAPI -Session $AdminSession -Method POST -Path "/groups/$GroupID/apply").JSON.data
	Assert-Near 0.625 ([double]$NoOpGroupApply.rate_multiplier) "no-op managed group apply result"
	$GroupStats = (Invoke-HTTPJSON -Session $ControlSession -Method GET -Url "$MockBaseUrl/control/stats").JSON
	Assert-Equal 1 ([int]$GroupStats.group_updates) "unchanged managed group rate must not be written upstream again"

    [void](Invoke-HTTPJSON -Session $ControlSession -Method POST -Url "$MockBaseUrl/control/101" -Body @{ billing_rate = 1.8 })
    [void](Invoke-AppAPI -Session $AdminSession -Method POST -Path "/accounts/$($Account101.id)/rate-sync")
    $TrackedGroup = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/groups/$GroupID/").JSON.data
    Assert-Near 0.725 ([double]$TrackedGroup.rate_multiplier) "source account rate change must update bound group"

    Write-Step "checking file-backed event pagination and name snapshots"
    $TwoEvents = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=1&page_size=2"
    Assert-Equal 1 ([int]$TwoEvents.JSON.data.page) "event page number"
    Assert-Equal 2 ([int]$TwoEvents.JSON.data.page_size) "event page size"
    Assert-Count 2 $TwoEvents.JSON.data.items "event page items"
    Assert-True ([int]$TwoEvents.JSON.data.total -gt 2) "event page must report total records"
    Assert-True ([bool]$TwoEvents.JSON.data.has_next) "first event page must report a next page"
    Assert-True (-not [bool]$TwoEvents.JSON.data.has_previous) "first event page must not report a previous page"
    Assert-True ($null -eq $TwoEvents.JSON.data.items[0].PSObject.Properties["owner_id"]) "event API must not expose owner IDs"
    $SecondEventPage = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=2&page_size=2"
    Assert-True ([bool]$SecondEventPage.JSON.data.has_previous) "second event page must report a previous page"
    $InvalidEventPage = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=0&page_size=2" -ExpectedStatus @(400)
    Assert-Equal "INVALID_PAGE" $InvalidEventPage.JSON.error.code "invalid event page validation"
    $InvalidEventPageSize = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=1&page_size=201" -ExpectedStatus @(400)
    Assert-Equal "INVALID_PAGE_SIZE" $InvalidEventPageSize.JSON.error.code "invalid event page-size validation"
    $LegacyLimit = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?limit=8"
    Assert-Equal 8 ([int]$LegacyLimit.JSON.data.page_size) "legacy limit query alias"

    Write-Step "checking audit log retention settings"
    $DefaultAuditLog = Invoke-AppAPI -Session $AdminSession -Method GET -Path "/settings/audit-log"
    Assert-Equal 14 ([int]$DefaultAuditLog.JSON.data.retention_days) "default audit log retention"
    Assert-True (-not [bool]$DefaultAuditLog.JSON.data.configured) "audit log retention must start unconfigured"
    $InvalidAuditLog = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/settings/audit-log" -ExpectedStatus @(400) -Body @{
        retention_days = 0
    }
    Assert-Equal "INVALID_AUDIT_LOG_RETENTION" $InvalidAuditLog.JSON.error.code "audit log retention validation"
    $UnconfiguredPurge = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/settings/audit-log/purge" -ExpectedStatus @(400)
    Assert-Equal "AUDIT_LOG_RETENTION_REQUIRED" $UnconfiguredPurge.JSON.error.code "purge requires saved retention"
    $SavedAuditLog = Invoke-AppAPI -Session $AdminSession -Method PUT -Path "/settings/audit-log" -Body @{
        retention_days = 7
    }
    Assert-True ([bool]$SavedAuditLog.JSON.data.configured) "audit log retention must be saved"
    Assert-Equal 7 ([int]$SavedAuditLog.JSON.data.retention_days) "saved audit log retention"

    $RenamedSitePayload = $SitePayload.Clone()
    $RenamedSitePayload.name = "QA fixture renamed"
    $RenamedSitePayload.api_key = ""
    [void](Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/sites/$SiteID/" -Body $RenamedSitePayload)
    $AllAdminEvents = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=1&page_size=200").JSON.data
    $AccountSnapshotEvents = @($AllAdminEvents.items | Where-Object {
        $null -ne $_.PSObject.Properties["account_id"] -and $_.account_id -eq $Account101.id -and $_.site_name -eq "QA fixture"
    })
    Assert-True ($AccountSnapshotEvents.Count -gt 0) "events written before a site rename must retain the original site name"
    Assert-Equal "Claude primary" $AccountSnapshotEvents[0].account_name "account name snapshot"

    Write-Step "checking tenant isolation and per-owner site uniqueness"
    $UserEmail = "user-$RunID@example.test"
    $UserPassword = "qa-user-password-2026"
    $CreatedUser = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/users" -ExpectedStatus @(201) -Body @{
        email = $UserEmail
        password = $UserPassword
        role = "user"
    }
    Assert-Equal "user" $CreatedUser.JSON.data.role "created tenant role"
    $UserSession = New-HTTPClientSession
    $UserLogin = Invoke-AppAPI -Session $UserSession -Method POST -Path "/auth/login" -Unprotected -Body @{
        email = $UserEmail
        password = $UserPassword
    }
    Assert-Equal ([string]$CreatedUser.JSON.data.id) ([string]$UserLogin.JSON.data.id) "tenant login identity"
    Assert-Count 0 (Invoke-AppAPI -Session $UserSession -Method GET -Path "/sites").JSON.data "new tenant site isolation"
    Assert-Count 0 (Invoke-AppAPI -Session $UserSession -Method GET -Path "/accounts").JSON.data "new tenant account isolation"
    $CrossSite = Invoke-AppAPI -Session $UserSession -Method POST -Path "/sites/$SiteID/sync" -ExpectedStatus @(404)
    Assert-Equal "NOT_FOUND" $CrossSite.JSON.error.code "cross-tenant site operation must look absent"
    $CrossAccount = Invoke-AppAPI -Session $UserSession -Method POST -Path "/accounts/$($Account101.id)/probe" -ExpectedStatus @(404)
    Assert-Equal "NOT_FOUND" $CrossAccount.JSON.error.code "cross-tenant account operation must look absent"
	$CrossBulkAccount = Invoke-AppAPI -Session $UserSession -Method PATCH -Path "/accounts/bulk-settings" -ExpectedStatus @(404) -Body @{
		account_ids = @([string]$Account101.id)
		priority = @{ enabled = $true }
	}
	Assert-Equal "NOT_FOUND" $CrossBulkAccount.JSON.error.code "cross-tenant bulk account operation must look absent"

    $TenantSitePayload = $SitePayload.Clone()
    $TenantSitePayload.name = "Tenant QA fixture"
    $TenantSite = Invoke-AppAPI -Session $UserSession -Method POST -Path "/sites" -Body $TenantSitePayload -ExpectedStatus @(201)
    Assert-True ([string]$TenantSite.JSON.data.id -ne $SiteID) "different owners must receive distinct site records"
    Assert-Count 1 (Invoke-AppAPI -Session $UserSession -Method GET -Path "/sites").JSON.data "tenant-owned site list"
    Assert-Count 2 (Invoke-AppAPI -Session $UserSession -Method GET -Path "/accounts").JSON.data "tenant-owned account inventory"
	$TenantAccounts = @(Invoke-AppAPI -Session $UserSession -Method GET -Path "/accounts").JSON.data
	$TenantGroups = @(Invoke-AppAPI -Session $UserSession -Method GET -Path "/groups").JSON.data
	Assert-Count 1 $TenantGroups "tenant-owned group inventory"
	Assert-True ([string]$TenantGroups[0].id -ne $GroupID) "tenant group ID must differ from administrator group ID"
	$TenantFilterOptions = (Invoke-AppAPI -Session $UserSession -Method GET -Path "/accounts/filter-options").JSON.data
	Assert-Count 1 $TenantFilterOptions.groups "tenant group filter options"
	Assert-Equal ([string]$TenantGroups[0].id) ([string]$TenantFilterOptions.groups[0].id) "tenant filter must only expose its own group"
	Assert-Count 0 (Invoke-AppAPI -Session $UserSession -Method GET -Path "/accounts?group_id=$GroupID").JSON.data "cross-tenant group filter must return no accounts"
	$CrossGroupRead = Invoke-AppAPI -Session $UserSession -Method GET -Path "/groups/$GroupID/" -ExpectedStatus @(404)
	Assert-Equal "NOT_FOUND" $CrossGroupRead.JSON.error.code "cross-tenant group read must look absent"
	$CrossGroupApply = Invoke-AppAPI -Session $UserSession -Method POST -Path "/groups/$GroupID/apply" -ExpectedStatus @(404)
	Assert-Equal "NOT_FOUND" $CrossGroupApply.JSON.error.code "cross-tenant group apply must look absent"
	$CrossGroupBinding = Invoke-AppAPI -Session $UserSession -Method PUT -Path "/groups/$($TenantGroups[0].id)/config" -ExpectedStatus @(400) -Body @{
		enabled = $true
		mode = "first"
		offset = 0
		expression = $null
		bindings = @([string]$Account101.id)
		apply = $false
	}
	Assert-Equal "INVALID_RATE_SOURCE" $CrossGroupBinding.JSON.error.code "cross-tenant source binding must be rejected"
    Assert-Count 1 (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/sites").JSON.data "administrator site list must exclude tenant site"
    Assert-Count 2 (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/accounts").JSON.data "administrator accounts must exclude tenant inventory"
    $AdminEventPage = (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/events?page=1&page_size=200").JSON.data
    $UserEventPage = (Invoke-AppAPI -Session $UserSession -Method GET -Path "/events?page=1&page_size=200").JSON.data
    Assert-Count 0 @($AdminEventPage.items | Where-Object { $null -ne $_.PSObject.Properties["site_id"] -and $_.site_id -eq $TenantSite.JSON.data.id }) "administrator event isolation"
    Assert-Count 0 @($UserEventPage.items | Where-Object { $null -ne $_.PSObject.Properties["site_id"] -and $_.site_id -eq $SiteID }) "tenant event isolation"
    Assert-True ([int]$UserEventPage.total -gt 0) "tenant must see its own activity events"

    $AuditFiles = @(Get-ChildItem -LiteralPath $AuditLogDir -File -Filter "audit-*.jsonl")
    Assert-True ($AuditFiles.Count -gt 0) "daily audit JSONL file must exist"
    $DiskEvents = @($AuditFiles | Get-Content | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | ForEach-Object { $_ | ConvertFrom-Json })
    Assert-True (@($DiskEvents | Where-Object { $_.owner_id -eq $Setup.JSON.data.id }).Count -gt 0) "administrator owner ID must be stored in JSONL"
    Assert-True (@($DiskEvents | Where-Object { $_.owner_id -eq $CreatedUser.JSON.data.id }).Count -gt 0) "tenant owner ID must be stored in JSONL"

    Write-Step "checking password-change session revocation"
    $NewUserPassword = "qa-user-password-updated-2026"
    [void](Invoke-AppAPI -Session $AdminSession -Method PATCH -Path "/users/$($CreatedUser.JSON.data.id)" -Body @{
        password = $NewUserPassword
    })
    $RevokedSession = Invoke-AppAPI -Session $UserSession -Method GET -Path "/auth/me" -ExpectedStatus @(401)
    Assert-Equal "SESSION_EXPIRED" $RevokedSession.JSON.error.code "password changes must revoke existing sessions"
    $UserSession = New-HTTPClientSession
    $Relogin = Invoke-AppAPI -Session $UserSession -Method POST -Path "/auth/login" -Unprotected -Body @{
        email = $UserEmail
        password = $NewUserPassword
    }
    Assert-Equal ([string]$CreatedUser.JSON.data.id) ([string]$Relogin.JSON.data.id) "updated password login"

    Write-Step "checking login failure throttling"
    $LoginSession = New-HTTPClientSession
    foreach ($Attempt in 1..5) {
        $Failure = Invoke-AppAPI -Session $LoginSession -Method POST -Path "/auth/login" -Unprotected -ExpectedStatus @(401) -Body @{
            email = $UserEmail
            password = "definitely-the-wrong-password"
        } -AdditionalHeaders @{ "X-Forwarded-For" = "198.51.100.$Attempt" }
        Assert-Equal "INVALID_CREDENTIALS" $Failure.JSON.error.code "login failure $Attempt"
    }
    $Blocked = Invoke-AppAPI -Session $LoginSession -Method POST -Path "/auth/login" -Unprotected -ExpectedStatus @(429) -Body @{
        email = $UserEmail
        password = "definitely-the-wrong-password"
    } -AdditionalHeaders @{ "X-Forwarded-For" = "198.51.100.99" }
    Assert-Equal "LOGIN_RATE_LIMITED" $Blocked.JSON.error.code "sixth login request must be rate limited"

    Write-Step "checking concurrent administrator mutation authorization"
    $RaceAdmin1Email = "race-admin-1-$RunID@example.test"
    $RaceAdmin2Email = "race-admin-2-$RunID@example.test"
    $RaceAdminPassword = "qa-race-admin-password-2026"
    $RaceAdmin1 = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/users" -Body @{
        email = $RaceAdmin1Email
        password = $RaceAdminPassword
        role = "admin"
    } -ExpectedStatus @(201)
    $RaceAdmin2 = Invoke-AppAPI -Session $AdminSession -Method POST -Path "/users" -Body @{
        email = $RaceAdmin2Email
        password = $RaceAdminPassword
        role = "admin"
    } -ExpectedStatus @(201)
    $RaceSession1 = New-HTTPClientSession
    $RaceSession2 = New-HTTPClientSession
    [void](Invoke-AppAPI -Session $RaceSession1 -Method POST -Path "/auth/login" -Unprotected -Body @{
        email = $RaceAdmin1Email
        password = $RaceAdminPassword
    })
    [void](Invoke-AppAPI -Session $RaceSession2 -Method POST -Path "/auth/login" -Unprotected -Body @{
        email = $RaceAdmin2Email
        password = $RaceAdminPassword
    })
    $DeleteRequest1 = [Net.Http.HttpRequestMessage]::new(
        [Net.Http.HttpMethod]::Delete,
        [Uri]"$script:AppBaseUrl/api/v1/users/$($RaceAdmin2.JSON.data.id)"
    )
    $DeleteRequest2 = [Net.Http.HttpRequestMessage]::new(
        [Net.Http.HttpMethod]::Delete,
        [Uri]"$script:AppBaseUrl/api/v1/users/$($RaceAdmin1.JSON.data.id)"
    )
    [void]$DeleteRequest1.Headers.TryAddWithoutValidation("X-CSRF-Token", (Get-CSRFToken -Session $RaceSession1 -BaseUrl $script:AppBaseUrl))
    [void]$DeleteRequest2.Headers.TryAddWithoutValidation("X-CSRF-Token", (Get-CSRFToken -Session $RaceSession2 -BaseUrl $script:AppBaseUrl))
    try {
        $DeleteTask1 = $RaceSession1.Client.SendAsync($DeleteRequest1)
        $DeleteTask2 = $RaceSession2.Client.SendAsync($DeleteRequest2)
        $DeleteResponse1 = $DeleteTask1.GetAwaiter().GetResult()
        $DeleteResponse2 = $DeleteTask2.GetAwaiter().GetResult()
        try {
            $DeleteStatuses = @([int]$DeleteResponse1.StatusCode, [int]$DeleteResponse2.StatusCode)
            Assert-Count 1 @($DeleteStatuses | Where-Object { $_ -eq 204 }) "only one concurrent mutual deletion may succeed"
            Assert-Count 1 @($DeleteStatuses | Where-Object { $_ -eq 401 -or $_ -eq 403 }) "the deleted administrator's in-flight request must be rejected"
        }
        finally {
            $DeleteResponse1.Dispose()
            $DeleteResponse2.Dispose()
        }
    }
    finally {
        $DeleteRequest1.Dispose()
        $DeleteRequest2.Dispose()
    }
    $UsersAfterRace = @((Invoke-AppAPI -Session $AdminSession -Method GET -Path "/users").JSON.data)
    foreach ($RaceUser in @($UsersAfterRace | Where-Object { $_.email -in @($RaceAdmin1Email, $RaceAdmin2Email) })) {
        [void](Invoke-AppAPI -Session $AdminSession -Method DELETE -Path "/users/$($RaceUser.id)" -ExpectedStatus @(204))
    }

    [void](Invoke-AppAPI -Session $AdminSession -Method DELETE -Path "/users/$($CreatedUser.JSON.data.id)" -ExpectedStatus @(204))
    Assert-Count 1 (Invoke-AppAPI -Session $AdminSession -Method GET -Path "/users").JSON.data "tenant deletion must cascade without violating tenant constraints"

    $RunSucceeded = $true
    Write-Host "[QA] PASS: all API acceptance checks completed"
}
catch {
    $Failure = $_
    Write-Host "[QA] FAIL: $($Failure.Exception.Message)" -ForegroundColor Red
    Show-LogTail -Name "application stdout" -Path $AppStdout
    Show-LogTail -Name "application stderr" -Path $AppStderr
    Show-LogTail -Name "mock stdout" -Path $MockStdout
    Show-LogTail -Name "mock stderr" -Path $MockStderr
	Show-LogTail -Name "migration 1 stderr" -Path $Migration1Stderr
	Show-LogTail -Name "migration 2 stderr" -Path $Migration2Stderr
	Show-LogTail -Name "migration 1 stdout" -Path $Migration1Stdout
	Show-LogTail -Name "migration 2 stdout" -Path $Migration2Stdout
    throw $Failure
}
finally {
    Stop-QAProcess -Process $AppProcess
    Stop-QAProcess -Process $MockProcess
    foreach ($Session in $Sessions) {
        try { $Session.Client.Dispose() } catch { }
        try { $Session.Handler.Dispose() } catch { }
    }
    if ($DatabaseCreated) {
        try {
            Invoke-PsqlCommand -ConnectionUrl $PostgresAdminUrl -Sql "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='$DatabaseName' AND pid<>pg_backend_pid()"
            Invoke-PsqlCommand -ConnectionUrl $PostgresAdminUrl -Sql "DROP DATABASE `"$DatabaseName`""
            Write-Step "dropped isolated database $DatabaseName"
        }
        catch {
            Write-Warning "could not drop isolated database $DatabaseName`: $($_.Exception.Message)"
        }
    }
    if (Test-Path -LiteralPath $TempRoot) {
        try { Remove-Item -LiteralPath $TempRoot -Recurse -Force } catch { Write-Warning "could not remove $TempRoot" }
    }
    if (-not $RunSucceeded) {
        Write-Host "[QA] cleanup completed after failure"
    }
}
