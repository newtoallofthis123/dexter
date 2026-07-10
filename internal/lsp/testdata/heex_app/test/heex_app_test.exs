defmodule HeexAppTest do
  use ExUnit.Case, async: true

  test "fixture modules compile" do
    assert HeexApp.SharedLib.Worker.nested_brace_call() == :ok
  end
end
