// Fixed AppKit bridge. Inputs are JSON arguments, never evaluated as script.
ObjC.import('AppKit');
function string(value) { return value ? ObjC.unwrap(value) : ''; }
function instance(app) {
    return {pid: Number(app.processIdentifier), launched_at: app.launchDate ? String(app.launchDate.timeIntervalSince1970) : '',
        executable: app.executableURL ? string(app.executableURL.path) : '',
        path: app.bundleURL ? string(app.bundleURL.path) : '', identity: string(app.bundleIdentifier)};
}
function run(argv) {
    var input = JSON.parse(argv[0]);
    if (input.operation === 'inspect') {
        var found = [], apps = $.NSWorkspace.sharedWorkspace.runningApplications;
        for (var i = 0; i < apps.count; i++) {
            var a = instance(apps.objectAtIndex(i));
            if (a.path === input.path || a.executable.indexOf(input.path + '/') === 0) found.push(a);
        }
        found.sort(function(a, b) { return a.pid - b.pid; });
        return JSON.stringify({instances: found});
    }
    if (input.operation === 'quit') {
        var app = $.NSRunningApplication.runningApplicationWithProcessIdentifier(input.instance.pid);
        if (!app || app.isNil()) return JSON.stringify({status: 'instance_changed'});
        var live = instance(app), expected = input.instance;
        if (live.pid !== expected.pid || live.launched_at !== expected.launched_at || live.executable !== expected.executable ||
            live.path !== input.path || live.identity !== input.identity) return JSON.stringify({status: 'instance_changed'});
        return JSON.stringify({status: app.terminate ? 'requested' : 'refused'});
    }
    throw Error('unsupported lifecycle operation');
}
