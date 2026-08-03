// Zabbix -> notifpwa webhook (LIMITED INFO / untrusted push server variant)
// https://github.com/vrepsaj/notifpwa
//
// Use this instead of the full version when the notifpwa instance is run by
// someone else / a server you don't fully trust. It intentionally sends only
// a generic "something happened, go check Zabbix" message:
//   - no host name / IP
//   - no trigger name, description, or ID
//   - no event ID or tags
//   - no operational data
//   - no update comments
//   - NO url / actions field, so the relay never learns your Zabbix domain
//
// Required Zabbix webhook media type parameters:
//   notifpwa_url         e.g. https://notify.example.com
//   notifpwa_room        the room to post to, e.g. "zabbix". Devices that
//                        subscribed to this room receive it.
//   notifpwa_secret      (optional) the per-subscriber room secret; sent as the
//                        X-Room-Secret header so only devices in the room whose
//                        secret matches receive the alert. Posting needs no token.
//   event_source, event_value, event_update_status, event_nseverity

// Maps Zabbix event_nseverity (0-6) to a notifpwa "urgency" level.
var SEVERITY_URGENCY = [
    'low',      // 0 Not classified
    'low',      // 1 Information
    'normal',   // 2 Warning
    'normal',   // 3 Average
    'high',     // 4 High
    'high',     // 5 Disaster
    'low'       // 6 Resolved
];

var SEVERITY_LABEL = [
    'unclassified',
    'informational',
    'warning',
    'average',
    'high',
    'disaster',
    'resolved'
];

try {
    Zabbix.log(4, '[ notifpwa Webhook (limited) ] Executed with params: ' + value);

    var params = JSON.parse(value);

    if (!params.notifpwa_url) {
        throw 'Cannot get notifpwa_url';
    }
    else {
        params.notifpwa_url = (params.notifpwa_url.endsWith('/'))
            ? params.notifpwa_url.slice(0, -1) : params.notifpwa_url;
    }

    if (!params.notifpwa_room) {
        throw 'Cannot get notifpwa_room';
    }

    if ([0, 1, 2, 3, 4].indexOf(parseInt(params.event_source)) === -1) {
        throw 'Incorrect "event_source" parameter given: "' + params.event_source + '".\nMust be 0-4.';
    }

    if (params.event_source !== '0') {
        params.event_nseverity = '0';
    }

    if (params.event_value !== '0' && params.event_value !== '1'
        && (params.event_source === '0' || params.event_source === '3')) {
        throw 'Incorrect "event_value" parameter given: "' + params.event_value + '".\nMust be 0 or 1.';
    }

    if (params.event_update_status !== '0' && params.event_update_status !== '1' && params.event_source === '0') {
        throw 'Incorrect "event_update_status" parameter given: "' + params.event_update_status + '".\nMust be 0 or 1.';
    }

    if (params.event_value == 0) {
        params.event_nseverity = '6';
    }

    if (!SEVERITY_URGENCY[params.event_nseverity]) {
        throw 'Incorrect "event_nseverity" parameter given: ' + params.event_nseverity + '\nMust be 0-5.';
    }

    var severity = SEVERITY_LABEL[params.event_nseverity];
    var title, message;

    if (params.event_value == 0 && params.event_update_status == 0) {
        // Resolved
        title = 'Zabbix: resolved';
        message = 'A previously reported issue was resolved. Check Zabbix for details.';
    }
    else if (params.event_update_status == 1) {
        // Update (ack/comment)
        title = 'Zabbix: event updated';
        message = 'An open issue was updated. Check Zabbix for details.';
    }
    else {
        // New problem
        title = 'Zabbix: ' + severity + ' issue';
        message = 'An unexpected ' + severity + ' issue occurred. Check Zabbix for details.';
    }

    var body = {
        title: title,
        body: message,
        // Fixed tag: repeated alerts replace the previous notification rather
        // than piling up, and don't reveal how many distinct triggers exist.
        tag: 'zabbix-alert',
        urgency: SEVERITY_URGENCY[params.event_nseverity]
        // Deliberately no "url" or "actions" here.
    };

    var req = new HttpRequest();

    if (typeof params.HTTPProxy === 'string' && params.HTTPProxy.trim() !== '') {
        req.setProxy(params.HTTPProxy);
    }

    req.addHeader('Content-Type: application/json');
    // Rooms need no API token; an optional per-subscriber secret filters delivery.
    if (typeof params.notifpwa_secret === 'string' && params.notifpwa_secret !== '') {
        req.addHeader('X-Room-Secret: ' + params.notifpwa_secret);
    }

    var resp = req.post(params.notifpwa_url + '/n/' + encodeURIComponent(params.notifpwa_room), JSON.stringify(body)),
        data = JSON.parse(resp);

    Zabbix.log(4, '[ notifpwa Webhook (limited) ] JSON: ' + JSON.stringify(body));
    Zabbix.log(4, '[ notifpwa Webhook (limited) ] Response: ' + resp);

    if (req.getStatus() >= 200 && req.getStatus() < 300 && typeof data.sent !== 'undefined') {
        return resp;
    }
    else {
        var message2 = ((typeof data.message === 'string') ? data.message : 'Unknown error');

        Zabbix.log(3, '[ notifpwa Webhook (limited) ] FAILED with response: ' + resp);
        throw message2 + '. For more details check zabbix server log.';
    }
}
catch (error) {
    Zabbix.log(3, '[ notifpwa Webhook (limited) ] ERROR: ' + error);
    throw 'Sending failed: ' + error;
}