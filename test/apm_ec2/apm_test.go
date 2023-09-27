// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT

//go:build !windows

package apm_ec2

import (
	"errors"
	"flag"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/aws/amazon-cloudwatch-agent-test/environment"
	"github.com/aws/amazon-cloudwatch-agent-test/test/metric"
	"github.com/aws/amazon-cloudwatch-agent-test/test/status"
	"github.com/aws/amazon-cloudwatch-agent-test/test/test_runner"
)

const testRetryCount = 3
const namespace = "AWS/APM"

var path, testId string

func init() {
	environment.RegisterEnvironmentMetaDataFlags()

	flag.StringVar(&path, "path", "./resources/run_java_application.sh", "path to java script file.")
	flag.StringVar(&testId, "test.id", "unknown", "testing id.")
}

type APMEC2TestRunner struct {
	test_runner.BaseTestRunner
}

func (runner *APMEC2TestRunner) Validate() status.TestGroupResult {
	return status.TestGroupResult{
		Name:        runner.GetTestName(),
		TestResults: []status.TestResult{},
	}
}

func (runner *APMEC2TestRunner) ValidateMetrics() status.TestGroupResult {
	metricsToFetch := metric.APMMetricNames
	testResults := make([]status.TestResult, len(metricsToFetch))
	instructions := metric.EC2ServerConsumerInstructions

	for i, metricName := range metricsToFetch {
		var testResult status.TestResult
		for j := 0; j < testRetryCount; j++ {
			testResult = metric.ValidateAPMMetric(runner.DimensionFactory, namespace, metricName, instructions)
			if testResult.Status == status.SUCCESSFUL {
				break
			}
			time.Sleep(15 * time.Second)
		}
		testResults[i] = testResult
	}

	return status.TestGroupResult{
		Name:        runner.GetTestName(),
		TestResults: testResults,
	}
}

func (runner *APMEC2TestRunner) GetTestName() string {
	return "APMTest/EC2"
}

func (runner *APMEC2TestRunner) GetAgentConfigFileName() string {
	return "config.json"
}

func (runner *APMEC2TestRunner) GetMeasuredMetrics() []string {
	return []string{}
}

var _ test_runner.ITestRunner = (*APMEC2TestRunner)(nil)

func TestHostedInAttributes(t *testing.T) {
	ec2TestRunner := &APMEC2TestRunner{test_runner.BaseTestRunner{}}
	testRunner := test_runner.TestRunner{TestRunner: ec2TestRunner}
	result := testRunner.Run()
	if result.GetStatus() != status.SUCCESSFUL {
		t.Fatal("Failed to install agent with APM config")
		result.Print()
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file %s does not exist.", path)
	}
	if output, err := exec.Command("bash", "-c", "sudo chmod +x "+path).Output(); err != nil {
		t.Fatalf("failed to execute chmod: %s.", string(output))
	}

	cmd := exec.Command("bash", "-c", "./"+path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, "TESTING_ID="+testId)

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start application: %v.", err)
	}

	success := false
	for i := 1; i < 10; i++ {
		if resp, err := http.Get("http://localhost:8080/api/gateway/owners/1"); err != nil {
			t.Logf("failed to send out request: %v.", err)
			time.Sleep(time.Duration(i*10) * time.Second)
		} else if resp.StatusCode >= 500 {
			t.Logf("returned failure response: %d", resp.StatusCode)
			time.Sleep(10000)
		} else {
			success = true
			t.Log("successfully sent out the request.")
		}
	}

	if !success {
		t.Fatalf("failed to call the test service.")
	}

	// Sleep 1 minute to let metrics and traces be exported.
	time.Sleep(1 * time.Minute)

	validateResult := ec2TestRunner.ValidateMetrics()
	if validateResult.GetStatus() != status.SUCCESSFUL {
		t.Fatal("Failed to install agent with APM config")
		validateResult.Print()
	}

	// Using context cancel doesn't kill child process, kill process group instead.
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatalf("failed to stop application: %v.", err)
	}
	cmd.Wait()
}
