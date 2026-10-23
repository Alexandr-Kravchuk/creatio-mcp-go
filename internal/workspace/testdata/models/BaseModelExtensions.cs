using System.Collections.Generic;
using System.ComponentModel.DataAnnotations;
using ATF.Repository;

namespace Acme.Models {

	public static class BaseModelExtensions{
		#region Methods: Public
		
		public static List<ValidationResult> ValidateModel(this BaseModel model) {
			List<ValidationResult> validationResult = new List<ValidationResult>();
			bool isValid = Validator.TryValidateObject(model, new ValidationContext(model), validationResult, true);
			return isValid ? new List<ValidationResult>() : validationResult;
		}
		
		#endregion
	}

}
