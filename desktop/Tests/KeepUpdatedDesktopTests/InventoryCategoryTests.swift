import XCTest
@testable import KeepUpdatedDesktop

final class InventoryCategoryTests: XCTestCase {
    private func pythonTool(_ owner: String = "pipx", name: String = "nano-pdf", environment: String = "/Tools/nano-pdf") -> InventoryItem {
        var value = DemoData.item("python-tool", name, "python-package", owner, "unknown", code: "online_query_required")
        value.path = environment + "/lib/python3.14/site-packages/nano_pdf-1.0.0.dist-info"
        value.realPath = value.path
        value.evidence = ["environment:" + environment]
        return value
    }

    func testDirectPythonToolsRemainManageableWithoutAConfirmedNewVersion() {
        for owner in ["pipx", "uv"] {
            for status in ["unknown", "current", "available"] {
                var tool = pythonTool(owner, name: "Nano.PDF")
                tool.update.status = status
                XCTAssertTrue(tool.isDirectPythonTool)
                XCTAssertTrue(tool.hasManagedUpdatePreview)
                XCTAssertEqual(tool.category, .python)
                XCTAssertEqual(tool.canUpdate, status == "available")
            }
        }
        var repeatedEvidence = pythonTool()
        repeatedEvidence.evidence += repeatedEvidence.evidence
        XCTAssertTrue(repeatedEvidence.isDirectPythonTool)
    }

    func testPythonDependenciesAndUnmanagedEnvironmentsAreNotDirectTools() {
        var dependency = pythonTool()
        dependency.name = "pydantic"
        XCTAssertFalse(dependency.isDirectPythonTool)
        XCTAssertFalse(dependency.hasManagedUpdatePreview)
        for owner in ["global-python", "gcloud", "project", "pip"] {
            let tool = pythonTool(owner)
            XCTAssertFalse(tool.isDirectPythonTool)
            XCTAssertFalse(tool.hasManagedUpdatePreview)
        }
        var environment = pythonTool()
        environment.kind = "python-environment"
        XCTAssertFalse(environment.isDirectPythonTool)
        XCTAssertFalse(environment.hasManagedUpdatePreview)
    }

    func testDirectPythonToolRequiresOneCanonicalContainedEnvironment() {
        let tool = pythonTool()
        var invalid = tool; invalid.evidence = []
        XCTAssertFalse(invalid.isDirectPythonTool)
        invalid = tool; invalid.evidence.append("environment:/Other/nano-pdf")
        XCTAssertFalse(invalid.isDirectPythonTool)
        invalid = tool; invalid.evidence = ["environment:Tools/nano-pdf"]
        XCTAssertFalse(invalid.isDirectPythonTool)
        invalid = tool; invalid.evidence = ["environment:/"]
        XCTAssertFalse(invalid.isDirectPythonTool)
        for path in ["Tools/nano-pdf/package", "/Tools/nano-pdf-extra/package", "/Tools/nano-pdf", "/Tools/nano-pdf/../other/package", "/Tools/nano-pdf//package", "/Tools/nano-pdf/./package"] {
            invalid = tool; invalid.path = path
            XCTAssertFalse(invalid.isDirectPythonTool, path)
        }
        invalid = tool; invalid.realPath = "/Outside/package"
        XCTAssertFalse(invalid.isDirectPythonTool)
        invalid = tool; invalid.realPath = "relative/package"
        XCTAssertFalse(invalid.isDirectPythonTool)
        invalid = tool; invalid.evidence = ["environment:/Tools/../nano-pdf"]
        XCTAssertFalse(invalid.isDirectPythonTool)
        for name in ["", "../nano-pdf", "nano pdf", "-nano-pdf", "nano-pdf/", "Nano.__PDF"] {
            invalid = tool; invalid.name = name
            XCTAssertFalse(invalid.isDirectPythonTool, name)
        }
    }

    func testLanguageRuntimesAndCommandLineToolsHaveSeparateCategories() {
        for name in ["node", "python", "python3", "java", "bun", "go"] {
            XCTAssertEqual(DemoData.item(name, name, "executable", "PATH").category, .runtimes)
        }
        for name in ["codex", "claude", "grok", "gcloud", "npm", "pnpm", "pipx", "uv", "hermes"] {
            XCTAssertEqual(DemoData.item(name, name, "executable", "PATH").category, .commandLine)
        }
        XCTAssertEqual(DemoData.item("retained", "Saved runtime", "runtime", "version-manager").category, .runtimes)
        XCTAssertEqual(DemoData.item("pin", "Volta project", "runtime-pin", "volta").category, .runtimes)
        for (owner, category) in [("npm", ItemCategory.npm), ("pnpm", .pnpm), ("bun", .bun)] {
            XCTAssertEqual(DemoData.item(owner, "example", "node-package", owner).category, category)
        }
        XCTAssertEqual(DemoData.item("formula", "python", "formula", "homebrew").category, .homebrew)
    }
}
