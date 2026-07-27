// Zabbix -> notifpwa full webhook
// https://github.com/vrepsaj/notifpwa
//
// Required Zabbix webhook media type parameters:
//   notifpwa_url        e.g. https://notify.example.com
//   notifpwa_token       API token printed on notifpwa first run / admin page
//   zabbix_url
//   event_source, event_value, event_update_status, event_nseverity, event_severity
//   event_name, event_id, event_time, event_date, event_recovery_time, event_recovery_date
//   event_update_time, event_update_date, event_update_user, event_update_action, event_update_message
//   event_opdata, event_tags, trigger_id, trigger_description
//   host_name, host_ip
//   alert_subject, alert_message
//   HTTPProxy (optional)

// Maps Zabbix event_nseverity (0-6) to a notifpwa "urgency" level
// (notifpwa only accepts: very-low / low / normal / high)
var SEVERITY_URGENCY = [
    'low',      // 0 Not classified
    'low',      // 1 Information
    'normal',   // 2 Warning
    'normal',   // 3 Average
    'high',     // 4 High
    'high',     // 5 Disaster
    'low'       // 6 Resolved
];

// Small severity tag prepended to the title so it's visible in a plain-text
// push notification (no colored embed like Discord has).
var SEVERITY_LABEL = [
    'Not classified',
    'Information',
    'Warning',
    'Average',
    'High',
    'Disaster',
    'Resolved'
];

function stringTruncate(str, len) {
    return str.length > len ? str.substring(0, len - 3) + '...' : str;
}

try {
    Zabbix.log(4, '[ notifpwa Webhook ] Executed with params: ' + value);

    var params = JSON.parse(value);

    if (!params.notifpwa_url) {
        throw 'Cannot get notifpwa_url';
    }
    else {
        params.notifpwa_url = (params.notifpwa_url.endsWith('/'))
            ? params.notifpwa_url.slice(0, -1) : params.notifpwa_url;
    }

    if (!params.notifpwa_token) {
        throw 'Cannot get notifpwa_token';
    }

    params.zabbix_url = (params.zabbix_url.endsWith('/'))
        ? params.zabbix_url.slice(0, -1) : params.zabbix_url;

    if ([0, 1, 2, 3, 4].indexOf(parseInt(params.event_source)) === -1) {
        throw 'Incorrect "event_source" parameter given: "' + params.event_source + '".\nMust be 0-4.';
    }

    // Set params to true for non trigger-based events.
    if (params.event_source !== '0') {
        params.use_default_message = 'true';
        params.event_nseverity = '0';
    }

    // Check {EVENT.VALUE} for trigger-based and internal events.
    if (params.event_value !== '0' && params.event_value !== '1'
        && (params.event_source === '0' || params.event_source === '3')) {
        throw 'Incorrect "event_value" parameter given: "' + params.event_value + '".\nMust be 0 or 1.';
    }

    // Check {EVENT.UPDATE.STATUS} only for trigger-based events.
    if (params.event_update_status !== '0' && params.event_update_status !== '1' && params.event_source === '0') {
        throw 'Incorrect "event_update_status" parameter given: "' + params.event_update_status + '".\nMust be 0 or 1.';
    }

    if (params.event_value == 0) {
        params.event_nseverity = '6';
    }

    if (!SEVERITY_URGENCY[params.event_nseverity]) {
        throw 'Incorrect "event_nseverity" parameter given: ' + params.event_nseverity + '\nMust be 0-5.';
    }

    // Link to the relevant Zabbix page, same logic as the Discord version.
    var eventUrl = (params.event_source === '0')
        ? params.zabbix_url + '/tr_events.php?triggerid=' + params.trigger_id +
            '&eventid=' + params.event_id
        : params.zabbix_url;

    var title, bodyLines = [];

    // Default message from {ALERT.MESSAGE}.
    if (params.use_default_message.toLowerCase() == 'true') {
        title = stringTruncate(params.alert_subject, 150);
        bodyLines.push(params.alert_message);
    }
    else {
        // Resolved message.
        if (params.event_value == 0 && params.event_update_status == 0) {
            title = stringTruncate('OK: ' + params.event_name, 150);
            bodyLines.push('Recovery time: ' + params.event_recovery_time + ' ' + params.event_recovery_date);
        }
        // Problem message.
        else if (params.event_value == 1 && params.event_update_status == 0) {
            title = stringTruncate('PROBLEM: ' + params.event_name, 150);
            bodyLines.push('Event time: ' + params.event_time + ' ' + params.event_date);
        }
        // Update message.
        else if (params.event_update_status == 1) {
            title = stringTruncate('UPDATE: ' + params.event_name, 150);
            var updateLine = params.event_update_user + ' ' + params.event_update_action + '.';
            if (params.event_update_message) {
                updateLine += ' Comment: ' + params.event_update_message;
            }
            bodyLines.push(updateLine);
            bodyLines.push('Update time: ' + params.event_update_time + ' ' + params.event_update_date);
        }

        bodyLines.push('Host: ' + params.host_name + ' [' + params.host_ip + ']');
        bodyLines.push('Severity: ' + params.event_severity);

        if (params.event_opdata) {
            bodyLines.push('Operational data: ' + stringTruncate(params.event_opdata, 300));
        }

        if (params.event_value == 1 && params.event_update_status == 0 && params.trigger_description) {
            bodyLines.push('Trigger description: ' + stringTruncate(params.trigger_description, 300));
        }

        var footer = 'Event ID: ' + params.event_id;
        if (params.event_tags) {
            footer += ' | Tags: ' + params.event_tags;
        }
        bodyLines.push(footer);
    }

    var body = {
        title: stringTruncate(SEVERITY_LABEL[params.event_nseverity] + ': ' + title, 200),
        body: stringTruncate(bodyLines.join('\n'), 1000),
        url: eventUrl,
        // Grouping/replacing tag: same trigger keeps updating one notification
        // instead of stacking a new one for every problem/update/resolve.
        tag: 'zabbix-' + (params.trigger_id || params.event_id),
        urgency: SEVERITY_URGENCY[params.event_nseverity],
        actions: [
            { title: 'Open in Zabbix', url: eventUrl }
        ]
    };

    var req = new HttpRequest();

    if (typeof params.HTTPProxy === 'string' && params.HTTPProxy.trim() !== '') {
        req.setProxy(params.HTTPProxy);
    }

    req.addHeader('Content-Type: application/json');
    req.addHeader('Authorization: Bearer ' + params.notifpwa_token);

    var resp = req.post(params.notifpwa_url + '/api/send', JSON.stringify(body)),
        data = JSON.parse(resp);

    Zabbix.log(4, '[ notifpwa Webhook ] JSON: ' + JSON.stringify(body));
    Zabbix.log(4, '[ notifpwa Webhook ] Response: ' + resp);

    if (req.getStatus() >= 200 && req.getStatus() < 300 && typeof data.sent !== 'undefined') {
        return resp;
    }
    else {
        var message = ((typeof data.message === 'string') ? data.message : 'Unknown error');

        Zabbix.log(3, '[ notifpwa Webhook ] FAILED with response: ' + resp);
        throw message + '. For more details check zabbix server log.';
    }
}
catch (error) {
    Zabbix.log(3, '[ notifpwa Webhook ] ERROR: ' + error);
    throw 'Sending failed: ' + error;
}