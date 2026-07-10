defmodule IntegrationAppTest do
  use ExUnit.Case, async: true

  test "fixture compiles and executes" do
    assert IntegrationApp.Page.render(3) == 6
  end
end
