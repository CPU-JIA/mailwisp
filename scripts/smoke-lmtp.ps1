param(
    [Parameter(Mandatory)]
    [string]$HostName,

    [Parameter(Mandatory)]
    [ValidateRange(1, 65535)]
    [int]$Port,

    [Parameter(Mandatory)]
    [string]$Recipient,

    [string]$Sender = 'sender@example.net',

    [string]$Subject = 'MailWisp smoke test',

    [string]$Body = 'Fast mail. Zero trace.'
)

$ErrorActionPreference = 'Stop'

foreach ($headerValue in @($Sender, $Recipient, $Subject)) {
    if ($headerValue.Contains("`r") -or $headerValue.Contains("`n")) {
        throw 'LMTP smoke header values must not contain CR or LF.'
    }
}

function Read-LMTPResponse {
    param(
        [Parameter(Mandatory)]
        [System.IO.StreamReader]$Reader,

        [Parameter(Mandatory)]
        [int]$ExpectedCode
    )

    $lines = [System.Collections.Generic.List[string]]::new()
    do {
        $line = $Reader.ReadLine()
        if ($null -eq $line -or $line.Length -lt 4) {
            throw "LMTP returned an incomplete response while expecting $ExpectedCode."
        }
        $actualCode = 0
        if (-not [int]::TryParse($line.Substring(0, 3), [ref]$actualCode) -or $actualCode -ne $ExpectedCode) {
            throw "LMTP returned '$line' while expecting $ExpectedCode."
        }
        $lines.Add($line)
        $continued = $line[3] -eq '-'
        if (-not $continued -and $line[3] -ne ' ') {
            throw "LMTP returned an invalid response separator in '$line'."
        }
    } while ($continued)

    return $lines
}

function Send-LMTPLine {
    param(
        [Parameter(Mandatory)]
        [System.IO.StreamWriter]$Writer,

        [Parameter(Mandatory)]
        [string]$Line
    )

    $Writer.WriteLine($Line)
    $Writer.Flush()
}

$client = [System.Net.Sockets.TcpClient]::new()
$reader = $null
$writer = $null
try {
    $client.Connect($HostName, $Port)
    $stream = $client.GetStream()
    $stream.ReadTimeout = 5000
    $stream.WriteTimeout = 5000
    $reader = [System.IO.StreamReader]::new($stream, [System.Text.Encoding]::ASCII, $false, 1024, $true)
    $writer = [System.IO.StreamWriter]::new($stream, [System.Text.Encoding]::ASCII, 1024, $true)
    $writer.NewLine = "`r`n"
    $writer.AutoFlush = $true

    Read-LMTPResponse -Reader $reader -ExpectedCode 220 | Out-Null
    Send-LMTPLine -Writer $writer -Line 'LHLO smoke.mailwisp.local'
    Read-LMTPResponse -Reader $reader -ExpectedCode 250 | Out-Null
    Send-LMTPLine -Writer $writer -Line "MAIL FROM:<$Sender>"
    Read-LMTPResponse -Reader $reader -ExpectedCode 250 | Out-Null
    Send-LMTPLine -Writer $writer -Line "RCPT TO:<$Recipient>"
    Read-LMTPResponse -Reader $reader -ExpectedCode 250 | Out-Null
    Send-LMTPLine -Writer $writer -Line 'DATA'
    Read-LMTPResponse -Reader $reader -ExpectedCode 354 | Out-Null

    $normalizedBody = $Body -replace "`r`n|`r|`n", "`n"
    $encodedBody = ($normalizedBody -split "`n" | ForEach-Object {
        if ($_.StartsWith('.')) {
            ".$_"
        } else {
            $_
        }
    }) -join "`r`n"
    $writer.Write("From: $Sender`r`nTo: $Recipient`r`nSubject: $Subject`r`n`r`n$encodedBody`r`n.`r`n")
    $writer.Flush()
    Read-LMTPResponse -Reader $reader -ExpectedCode 250 | Out-Null
    Send-LMTPLine -Writer $writer -Line 'QUIT'
    Read-LMTPResponse -Reader $reader -ExpectedCode 221 | Out-Null

    [pscustomobject]@{
        Status    = 'delivered'
        Host      = $HostName
        Port      = $Port
        Recipient = $Recipient
    }
} finally {
    if ($null -ne $reader) {
        $reader.Dispose()
    }
    if ($null -ne $writer) {
        $writer.Dispose()
    }
    $client.Dispose()
}
